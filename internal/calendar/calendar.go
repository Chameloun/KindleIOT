package calendar

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"kindleiot/internal/config"
	"kindleiot/internal/mqtt"
)

// Event represents an upcoming calendar event parsed from an iCal (.ics) feed.
type Event struct {
	Summary  string    `json:"summary"`
	Location string    `json:"location,omitempty"`
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	AllDay   bool      `json:"all_day"`
}

// Service periodically fetches and merges iCal (.ics) feeds from configured URLs.
type Service struct {
	mu       sync.RWMutex
	cfg      config.CalendarConfig
	client   *http.Client
	events   []Event
	onUpdate func()
	stopCh   chan struct{}
}

// NewService creates a new iCal calendar polling service.
func NewService(cfg config.CalendarConfig, onUpdate func()) *Service {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs:    mqtt.RootCAPool(),
			MinVersion: tls.VersionTLS12,
		},
	}
	return &Service{
		cfg: cfg,
		client: &http.Client{
			Transport: tr,
			Timeout:   15 * time.Second,
		},
		onUpdate: onUpdate,
	}
}

// SetHTTPClient overrides the HTTP client used for calendar downloads (primarily for testing).
func (s *Service) SetHTTPClient(c *http.Client) {
	if c == nil {
		return
	}
	s.mu.Lock()
	s.client = c
	s.mu.Unlock()
}

// Start launches background periodic calendar polling (every 15 minutes).
func (s *Service) Start() {
	s.mu.Lock()
	if s.stopCh != nil {
		s.mu.Unlock()
		return
	}
	stopCh := make(chan struct{})
	s.stopCh = stopCh
	s.mu.Unlock()

	go func() {
		s.FetchNow()
		tick := time.NewTicker(15 * time.Minute)
		defer tick.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-tick.C:
				s.FetchNow()
			}
		}
	}()
}

// Stop terminates background calendar polling.
func (s *Service) Stop() {
	s.mu.Lock()
	if s.stopCh != nil {
		close(s.stopCh)
		s.stopCh = nil
	}
	s.mu.Unlock()
}

// Reconfigure updates calendar URLs/enabled flag and refreshes immediately if enabled.
func (s *Service) Reconfigure(newCfg config.CalendarConfig) {
	s.mu.Lock()
	s.cfg = newCfg
	if !newCfg.Enabled {
		s.events = nil
	}
	s.mu.Unlock()

	if newCfg.Enabled {
		go s.FetchNow()
	}
}

// Events returns a copy of upcoming cached calendar events.
func (s *Service) Events() []Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.events) == 0 {
		return nil
	}
	out := make([]Event, len(s.events))
	copy(out, s.events)
	return out
}

// FetchNow synchronously downloads all configured iCal feeds and updates the cached event list.
func (s *Service) FetchNow() {
	s.mu.RLock()
	cfg := s.cfg
	client := s.client
	s.mu.RUnlock()

	if !cfg.Enabled || len(cfg.URLs) == 0 {
		return
	}

	now := time.Now()
	loc := now.Location()
	var merged []Event
	seen := make(map[string]bool)
	anySuccess := false

	for _, rawURL := range cfg.URLs {
		u := normalizeFeedURL(rawURL)
		if u == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			cancel()
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			continue
		}
		if resp.StatusCode == http.StatusOK {
			anySuccess = true
			evs := ParseICS(resp.Body, now, loc)
			for _, ev := range evs {
				key := fmt.Sprintf("%d|%v|%s|%s", ev.Start.Unix(), ev.AllDay, ev.Summary, ev.Location)
				if !seen[key] {
					seen[key] = true
					merged = append(merged, ev)
				}
			}
		}
		resp.Body.Close()
		cancel()
	}

	if !anySuccess {
		return
	}

	sortEvents(merged)
	if len(merged) > 15 {
		merged = merged[:15]
	}

	s.mu.Lock()
	s.events = merged
	cb := s.onUpdate
	s.mu.Unlock()

	if cb != nil {
		cb()
	}
}

func normalizeFeedURL(raw string) string {
	u := strings.TrimSpace(raw)
	if u == "" {
		return ""
	}
	if len(u) >= 9 && strings.EqualFold(u[:9], "webcal://") {
		return "https://" + u[9:]
	}
	return u
}

type rawVEvent struct {
	summary    string
	location   string
	dtStartRaw string
	dtStartPar string
	dtEndRaw   string
	dtEndPar   string
	rrule      string
}

// ParseICS parses an iCalendar (.ics) stream and returns upcoming events in [today 00:00, now+14d], sorted chronologically.
func ParseICS(r io.Reader, now time.Time, loc *time.Location) []Event {
	if loc == nil {
		loc = now.Location()
	}
	lines := unfoldICS(r)

	startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	endOfWindow := now.Add(14 * 24 * time.Hour)

	var out []Event
	var cur *rawVEvent

	for _, line := range lines {
		upper := strings.ToUpper(strings.TrimSpace(line))
		if upper == "BEGIN:VEVENT" {
			cur = &rawVEvent{}
			continue
		}
		if upper == "END:VEVENT" {
			if cur != nil {
				out = append(out, expandVEvent(cur, startOfToday, endOfWindow, loc)...)
				cur = nil
			}
			continue
		}
		if cur == nil {
			continue
		}

		colon := strings.IndexByte(line, ':')
		if colon <= 0 {
			continue
		}
		propPart := line[:colon]
		val := line[colon+1:]

		name := propPart
		params := ""
		if semi := strings.IndexByte(propPart, ';'); semi >= 0 {
			name = propPart[:semi]
			params = propPart[semi+1:]
		}
		switch strings.ToUpper(strings.TrimSpace(name)) {
		case "SUMMARY":
			cur.summary = unescapeICS(val)
		case "LOCATION":
			cur.location = unescapeICS(val)
		case "DTSTART":
			cur.dtStartRaw = strings.TrimSpace(val)
			cur.dtStartPar = params
		case "DTEND":
			cur.dtEndRaw = strings.TrimSpace(val)
			cur.dtEndPar = params
		case "RRULE":
			cur.rrule = strings.TrimSpace(val)
		}
	}

	sortEvents(out)
	if len(out) > 15 {
		out = out[:15]
	}
	return out
}

func unfoldICS(r io.Reader) []string {
	scanner := bufio.NewScanner(r)
	// Allow long ICS lines up to 1MB.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var lines []string
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r\n")
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			if len(lines) > 0 {
				lines[len(lines)-1] += line[1:]
			}
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func unescapeICS(s string) string {
	s = strings.ReplaceAll(s, `\n`, " ")
	s = strings.ReplaceAll(s, `\N`, " ")
	s = strings.ReplaceAll(s, `\,`, ",")
	s = strings.ReplaceAll(s, `\;`, ";")
	s = strings.ReplaceAll(s, `\\`, `\`)
	return strings.TrimSpace(s)
}

func parseICSTime(raw, params string, fallbackLoc *time.Location) (time.Time, bool, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false, false
	}
	loc := fallbackLoc
	if tzid := extractParam(params, "TZID"); tzid != "" {
		if loaded, err := time.LoadLocation(tzid); err == nil {
			loc = loaded
		}
	}

	isDateParam := strings.Contains(strings.ToUpper(params), "VALUE=DATE")
	if len(raw) == 8 || (isDateParam && !strings.Contains(raw, "T")) {
		if len(raw) >= 8 {
			if t, err := time.ParseInLocation("20060102", raw[:8], fallbackLoc); err == nil {
				return t, true, true
			}
		}
		return time.Time{}, false, false
	}

	if strings.HasSuffix(raw, "Z") {
		if t, err := time.ParseInLocation("20060102T150405", strings.TrimSuffix(raw, "Z"), time.UTC); err == nil {
			return t.In(fallbackLoc), false, true
		}
		return time.Time{}, false, false
	}

	if t, err := time.ParseInLocation("20060102T150405", raw, loc); err == nil {
		return t.In(fallbackLoc), false, true
	}
	return time.Time{}, false, false
}

func extractParam(params, key string) string {
	if params == "" {
		return ""
	}
	upperKey := strings.ToUpper(key) + "="
	for _, p := range strings.Split(params, ";") {
		if len(p) > len(upperKey) && strings.EqualFold(p[:len(upperKey)], upperKey) {
			return strings.Trim(p[len(upperKey):], `"`)
		}
	}
	return ""
}

func expandVEvent(raw *rawVEvent, startOfToday, endOfWindow time.Time, loc *time.Location) []Event {
	if strings.TrimSpace(raw.summary) == "" {
		raw.summary = "Událost"
	}
	dtStart, allDay, ok := parseICSTime(raw.dtStartRaw, raw.dtStartPar, loc)
	if !ok {
		return nil
	}
	dtEnd, _, okEnd := parseICSTime(raw.dtEndRaw, raw.dtEndPar, loc)
	var dur time.Duration
	if okEnd && dtEnd.After(dtStart) {
		dur = dtEnd.Sub(dtStart)
	} else if allDay {
		dur = 24 * time.Hour
	}

	inWindow := func(st, en time.Time) bool {
		if st.After(endOfWindow) {
			return false
		}
		checkEnd := en
		if checkEnd.IsZero() || !checkEnd.After(st) {
			return !st.Before(startOfToday)
		}
		return checkEnd.After(startOfToday)
	}

	if raw.rrule == "" {
		if inWindow(dtStart, dtStart.Add(dur)) {
			return []Event{{
				Summary:  raw.summary,
				Location: raw.location,
				Start:    dtStart,
				End:      dtStart.Add(dur),
				AllDay:   allDay,
			}}
		}
		return nil
	}

	freq, interval, until, hasUntil := parseRRule(raw.rrule, loc)
	var out []Event

	switch freq {
	case "YEARLY":
		for y := startOfToday.Year(); y <= endOfWindow.Year()+1; y++ {
			occ := time.Date(y, dtStart.Month(), dtStart.Day(), dtStart.Hour(), dtStart.Minute(), dtStart.Second(), 0, loc)
			if occ.Before(dtStart) {
				continue
			}
			if hasUntil && occ.After(until) {
				continue
			}
			occEnd := occ.Add(dur)
			if inWindow(occ, occEnd) {
				out = append(out, Event{
					Summary:  raw.summary,
					Location: raw.location,
					Start:    occ,
					End:      occEnd,
					AllDay:   allDay,
				})
			}
		}

	case "WEEKLY":
		if interval <= 0 {
			interval = 1
		}
		stepDays := 7 * interval
		searchFrom := startOfToday.Add(-dur)
		k := 0
		if searchFrom.After(dtStart) {
			daysDiff := int(searchFrom.Sub(dtStart).Hours() / 24)
			if daysDiff > stepDays {
				k = (daysDiff / stepDays) * stepDays
			}
		}
		for ; ; k += stepDays {
			occ := dtStart.AddDate(0, 0, k)
			if occ.After(endOfWindow) {
				break
			}
			if hasUntil && occ.After(until) {
				break
			}
			occEnd := occ.Add(dur)
			if inWindow(occ, occEnd) {
				out = append(out, Event{
					Summary:  raw.summary,
					Location: raw.location,
					Start:    occ,
					End:      occEnd,
					AllDay:   allDay,
				})
			}
		}

	default:
		if inWindow(dtStart, dtStart.Add(dur)) {
			out = append(out, Event{
				Summary:  raw.summary,
				Location: raw.location,
				Start:    dtStart,
				End:      dtStart.Add(dur),
				AllDay:   allDay,
			})
		}
	}
	return out
}

func parseRRule(rule string, loc *time.Location) (freq string, interval int, until time.Time, hasUntil bool) {
	interval = 1
	for _, part := range strings.Split(rule, ";") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		k := strings.ToUpper(kv[0])
		v := strings.TrimSpace(kv[1])
		switch k {
		case "FREQ":
			freq = strings.ToUpper(v)
		case "INTERVAL":
			if iv, err := strconv.Atoi(v); err == nil && iv > 0 {
				interval = iv
			}
		case "UNTIL":
			if ut, _, ok := parseICSTime(v, "", loc); ok {
				until = ut
				hasUntil = true
			}
		}
	}
	return freq, interval, until, hasUntil
}

func sortEvents(evs []Event) {
	sort.Slice(evs, func(i, j int) bool {
		if !evs[i].Start.Equal(evs[j].Start) {
			return evs[i].Start.Before(evs[j].Start)
		}
		return evs[i].Summary < evs[j].Summary
	})
}
