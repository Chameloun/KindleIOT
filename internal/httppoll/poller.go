package httppoll

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"kindleiot/internal/config"
	"kindleiot/internal/mqtt"
)

// NewHTTPClient returns a shared TLS 1.2+ HTTP client trusting KindleIOT's embedded CA bundle.
func NewHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs:    mqtt.RootCAPool(),
			MinVersion: tls.VersionTLS12,
		},
	}
	return &http.Client{
		Transport: tr,
		Timeout:   timeout,
	}
}

// ExpandWebhookTemplate replaces {{state}} and {{state_lower}} placeholders inside URL or body strings.
func ExpandWebhookTemplate(raw, state string) string {
	if raw == "" {
		return ""
	}
	r := strings.NewReplacer(
		"{{state}}", state,
		"{{state_lower}}", strings.ToLower(state),
	)
	return r.Replace(raw)
}

// FireWebhook executes an HTTP GET/POST/PUT webhook call, returning nil on any 2xx response.
func FireWebhook(ctx context.Context, client *http.Client, method, rawURL, body string, headers map[string]string, state string) error {
	if client == nil {
		client = NewHTTPClient(8 * time.Second)
	}
	urlStr := strings.TrimSpace(ExpandWebhookTemplate(rawURL, state))
	if urlStr == "" {
		return fmt.Errorf("empty webhook URL")
	}
	payload := ExpandWebhookTemplate(body, state)
	m := strings.ToUpper(strings.TrimSpace(method))
	if m == "" {
		if payload != "" {
			m = http.MethodPost
		} else {
			m = http.MethodGet
		}
	}

	var bodyReader io.Reader
	if payload != "" {
		bodyReader = strings.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, m, urlStr, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "KindleIOT/1.0")
	hasCT := false
	for k, v := range headers {
		if strings.EqualFold(k, "Content-Type") {
			hasCT = true
		}
		req.Header.Set(k, v)
	}
	trimmed := strings.TrimSpace(payload)
	if !hasCT && (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook HTTP %d", resp.StatusCode)
	}
	return nil
}

type pollTarget struct {
	widget   config.WidgetConfig
	lastPoll time.Time
}

// Service periodically polls widgets configured with HTTPURL and writes responses into local MQTT topics.
type Service struct {
	mu      sync.Mutex
	cfg     *config.Config
	mq      *mqtt.Manager
	client  *http.Client
	targets map[string]*pollTarget
	stopCh  chan struct{}
}

// NewService creates a new HTTP/JSON widget polling service.
func NewService(cfg *config.Config, mq *mqtt.Manager) *Service {
	s := &Service{
		cfg:     cfg,
		mq:      mq,
		client:  NewHTTPClient(10 * time.Second),
		targets: make(map[string]*pollTarget),
	}
	s.syncTargetsLocked(cfg)
	return s
}

// SetHTTPClient overrides the HTTP client (primarily for unit tests).
func (s *Service) SetHTTPClient(c *http.Client) {
	if c == nil {
		return
	}
	s.mu.Lock()
	s.client = c
	s.mu.Unlock()
}

func collectHTTPWidgets(cfg *config.Config) []config.WidgetConfig {
	if cfg == nil {
		return nil
	}
	var out []config.WidgetConfig
	for _, p := range cfg.Pages {
		for _, w := range p.Widgets {
			if strings.TrimSpace(w.HTTPURL) != "" && strings.TrimSpace(w.StateTopic) != "" {
				out = append(out, w)
			}
		}
		if p.Floorplan != nil {
			for _, pin := range p.Floorplan.Pins {
				if strings.TrimSpace(pin.Widget.HTTPURL) != "" && strings.TrimSpace(pin.Widget.StateTopic) != "" {
					out = append(out, pin.Widget)
				}
			}
		}
	}
	return out
}

func (s *Service) syncTargetsLocked(cfg *config.Config) []config.WidgetConfig {
	widgets := collectHTTPWidgets(cfg)
	active := make(map[string]struct{}, len(widgets))
	var pollNow []config.WidgetConfig

	for _, w := range widgets {
		active[w.ID] = struct{}{}
		cur, ok := s.targets[w.ID]
		if !ok {
			s.targets[w.ID] = &pollTarget{widget: w}
			pollNow = append(pollNow, w)
			continue
		}
		changed := cur.widget.HTTPURL != w.HTTPURL || cur.widget.StateTopic != w.StateTopic
		cur.widget = w
		if changed {
			cur.lastPoll = time.Time{}
			pollNow = append(pollNow, w)
		}
	}
	for id := range s.targets {
		if _, ok := active[id]; !ok {
			delete(s.targets, id)
		}
	}
	return pollNow
}

// Start begins background HTTP polling (evaluated every 2 seconds).
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
		s.PollNow()
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-tick.C:
				s.pollDue(time.Now())
			}
		}
	}()
}

// Stop terminates background HTTP polling.
func (s *Service) Stop() {
	s.mu.Lock()
	if s.stopCh != nil {
		close(s.stopCh)
		s.stopCh = nil
	}
	s.mu.Unlock()
}

// Reconfigure updates HTTP-polled widgets and triggers immediate fetches for new/changed URLs.
func (s *Service) Reconfigure(cfg *config.Config) {
	s.mu.Lock()
	s.cfg = cfg
	immediate := s.syncTargetsLocked(cfg)
	s.mu.Unlock()

	for _, w := range immediate {
		w := w
		go s.fetchWidget(w)
	}
}

// PollNow immediately polls every HTTP-configured widget once.
func (s *Service) PollNow() {
	s.mu.Lock()
	list := make([]config.WidgetConfig, 0, len(s.targets))
	now := time.Now()
	for _, tgt := range s.targets {
		tgt.lastPoll = now
		list = append(list, tgt.widget)
	}
	s.mu.Unlock()

	for _, w := range list {
		s.fetchWidget(w)
	}
}

func (s *Service) pollDue(now time.Time) {
	s.mu.Lock()
	var due []config.WidgetConfig
	for _, tgt := range s.targets {
		interval := tgt.widget.HTTPIntervalSec
		if interval <= 0 {
			interval = 60
		}
		if tgt.lastPoll.IsZero() || now.Sub(tgt.lastPoll) >= time.Duration(interval)*time.Second {
			tgt.lastPoll = now
			due = append(due, tgt.widget)
		}
	}
	s.mu.Unlock()

	for _, w := range due {
		s.fetchWidget(w)
	}
}

func (s *Service) fetchWidget(w config.WidgetConfig) {
	s.mu.Lock()
	client := s.client
	mq := s.mq
	s.mu.Unlock()
	if mq == nil || strings.TrimSpace(w.HTTPURL) == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.HTTPURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", "KindleIOT/1.0")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	for k, v := range w.HTTPHeaders {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return
	}
	body := strings.TrimSpace(string(raw))
	mq.SetLocalValue(w.BrokerID, w.StateTopic, body)
}
