package spot

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"kindleiot/internal/config"
	"kindleiot/internal/mqtt"
)

const defaultEndpoint = "https://spotovaelektrina.cz/api/v1/price/get-prices-json"

type apiHour struct {
	Hour     int     `json:"hour"`
	PriceEUR float64 `json:"priceEur"`
	PriceCZK float64 `json:"priceCZK"`
	Level    string  `json:"level"`
	LevelNum int     `json:"levelNum"`
}

type apiResponse struct {
	HoursToday    []apiHour `json:"hoursToday"`
	HoursTomorrow []apiHour `json:"hoursTomorrow"`
}

// HourPrice represents a single hourly spot electricity slot.
type HourPrice struct {
	Hour     int     `json:"hour"`
	RawCZK   float64 `json:"raw_czk"`   // CZK/kWh base OTE market price
	PriceCZK float64 `json:"price_czk"` // Final CZK/kWh including VAT & distribution fee
	PriceEUR float64 `json:"price_eur"` // EUR/MWh OTE market price
	Level    string  `json:"level"`     // low, medium, high
	LevelNum int     `json:"level_num"`
}

// DisplayValue returns the numeric price formatted in the requested currency ("EUR" = €/MWh, else Kč/kWh).
func (h HourPrice) DisplayValue(currency string) float64 {
	if strings.EqualFold(currency, "EUR") {
		return h.PriceEUR
	}
	return h.PriceCZK
}

// Snapshot holds today & tomorrow spot prices plus summary metrics for UI and API consumers.
type Snapshot struct {
	Available           bool        `json:"available"`
	Enabled             bool        `json:"enabled"`
	Currency            string      `json:"currency"` // "CZK" or "EUR"
	Unit                string      `json:"unit"`     // "Kč/kWh" or "€/MWh"
	Current             *HourPrice  `json:"current,omitempty"`
	Today               []HourPrice `json:"today,omitempty"`
	Tomorrow            []HourPrice `json:"tomorrow,omitempty"`
	MinToday            float64     `json:"min_today"`
	MaxToday            float64     `json:"max_today"`
	AvgToday            float64     `json:"avg_today"`
	CheapestHour        int         `json:"cheapest_hour"`
	CheapestWindowStart int         `json:"cheapest_window_start"`
	UpdatedAt           time.Time   `json:"updated_at"`
	LastError           string      `json:"last_error,omitempty"`
}

// Service periodically fetches Czech OTE electricity spot prices from spotovaelektrina.cz.
type Service struct {
	mu          sync.RWMutex
	cfg         config.SpotPriceConfig
	client      *http.Client
	baseURL     string
	rawToday    []apiHour
	rawTomorrow []apiHour
	updatedAt   time.Time
	lastError   string
	onUpdate    func(Snapshot)
	stopCh      chan struct{}
}

// NewService creates a new Czech OTE electricity spot price service.
func NewService(cfg config.SpotPriceConfig, onUpdate func(Snapshot)) *Service {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs:    mqtt.RootCAPool(),
			MinVersion: tls.VersionTLS12,
		},
	}
	return &Service{
		cfg:     cfg,
		baseURL: defaultEndpoint,
		client: &http.Client{
			Transport: tr,
			Timeout:   10 * time.Second,
		},
		onUpdate: onUpdate,
	}
}

// SetBaseURL overrides the API endpoint URL (primarily for unit tests).
func (s *Service) SetBaseURL(u string) {
	if strings.TrimSpace(u) == "" {
		return
	}
	s.mu.Lock()
	s.baseURL = u
	s.mu.Unlock()
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

// Start launches background periodic polling (every 30 minutes).
func (s *Service) Start() {
	s.mu.Lock()
	if s.stopCh != nil {
		s.mu.Unlock()
		return
	}
	stopCh := make(chan struct{})
	s.stopCh = stopCh
	enabled := s.cfg.Enabled
	s.mu.Unlock()

	go func() {
		if enabled {
			s.FetchNow()
		}
		tick := time.NewTicker(30 * time.Minute)
		defer tick.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-tick.C:
				s.mu.RLock()
				en := s.cfg.Enabled
				s.mu.RUnlock()
				if en {
					s.FetchNow()
				}
			}
		}
	}()
}

// Stop terminates background polling.
func (s *Service) Stop() {
	s.mu.Lock()
	if s.stopCh != nil {
		close(s.stopCh)
		s.stopCh = nil
	}
	s.mu.Unlock()
}

// Reconfigure updates pricing parameters (VAT, currency, distribution fee) and fetches if newly enabled.
func (s *Service) Reconfigure(newCfg config.SpotPriceConfig) {
	s.mu.Lock()
	wasEnabled := s.cfg.Enabled
	changed := s.cfg.Currency != newCfg.Currency ||
		s.cfg.EffectiveVAT() != newCfg.EffectiveVAT() ||
		s.cfg.DistributionFee != newCfg.DistributionFee ||
		wasEnabled != newCfg.Enabled
	s.cfg = newCfg
	hasData := len(s.rawToday) > 0
	cb := s.onUpdate
	s.mu.Unlock()

	if !newCfg.Enabled {
		if changed && cb != nil {
			cb(s.Snapshot())
		}
		return
	}
	if !hasData || !wasEnabled {
		go s.FetchNow()
		return
	}
	if changed && cb != nil {
		cb(s.Snapshot())
	}
}

// FetchNow immediately downloads the latest OTE spot price schedule.
func (s *Service) FetchNow() {
	s.mu.RLock()
	cfg := s.cfg
	client := s.client
	endpoint := s.baseURL
	cb := s.onUpdate
	s.mu.RUnlock()

	if !cfg.Enabled {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		s.setError(err.Error())
		return
	}
	req.Header.Set("User-Agent", "KindleIOT/1.0")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		s.setError(err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		s.setError(fmt.Sprintf("HTTP %d", resp.StatusCode))
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		s.setError(err.Error())
		return
	}
	var parsed apiResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		s.setError("invalid JSON: " + err.Error())
		return
	}
	if len(parsed.HoursToday) == 0 {
		s.setError("empty hoursToday")
		return
	}

	s.mu.Lock()
	s.rawToday = parsed.HoursToday
	s.rawTomorrow = parsed.HoursTomorrow
	s.updatedAt = time.Now()
	s.lastError = ""
	s.mu.Unlock()

	if cb != nil {
		cb(s.Snapshot())
	}
}

func (s *Service) setError(msg string) {
	s.mu.Lock()
	s.lastError = msg
	cb := s.onUpdate
	s.mu.Unlock()
	if cb != nil {
		cb(s.Snapshot())
	}
}

// Snapshot returns the current spot price view evaluated for time.Now().
func (s *Service) Snapshot() Snapshot {
	return s.SnapshotAt(time.Now())
}

// SnapshotAt returns spot prices evaluated at a specific timestamp (selecting the matching Current hour).
func (s *Service) SnapshotAt(now time.Time) Snapshot {
	s.mu.RLock()
	cfg := s.cfg
	rawToday := append([]apiHour(nil), s.rawToday...)
	rawTomorrow := append([]apiHour(nil), s.rawTomorrow...)
	updatedAt := s.updatedAt
	lastErr := s.lastError
	s.mu.RUnlock()

	cur := "CZK"
	unit := "Kč/kWh"
	if strings.EqualFold(cfg.Currency, "EUR") {
		cur = "EUR"
		unit = "€/MWh"
	}

	snap := Snapshot{
		Enabled:   cfg.Enabled,
		Currency:  cur,
		Unit:      unit,
		UpdatedAt: updatedAt,
		LastError: lastErr,
	}
	if len(rawToday) == 0 {
		return snap
	}

	vatMul := 1.0 + cfg.EffectiveVAT()/100.0
	snap.Today = convertDay(rawToday, vatMul, cfg.DistributionFee, cur)
	if len(rawTomorrow) > 0 {
		snap.Tomorrow = convertDay(rawTomorrow, vatMul, cfg.DistributionFee, cur)
	}
	snap.Available = true

	var sum float64
	minVal := snap.Today[0].DisplayValue(cur)
	maxVal := minVal
	cheapestHr := snap.Today[0].Hour
	curHour := now.Hour()

	for i := range snap.Today {
		hp := snap.Today[i]
		val := hp.DisplayValue(cur)
		sum += val
		if val < minVal {
			minVal = val
			cheapestHr = hp.Hour
		}
		if val > maxVal {
			maxVal = val
		}
		if hp.Hour == curHour {
			cp := hp
			snap.Current = &cp
		}
	}
	if snap.Current == nil && len(snap.Today) > 0 {
		cp := snap.Today[len(snap.Today)-1]
		snap.Current = &cp
	}

	snap.MinToday = minVal
	snap.MaxToday = maxVal
	snap.AvgToday = sum / float64(len(snap.Today))
	snap.CheapestHour = cheapestHr

	// Find cheapest contiguous 3-hour window
	if len(snap.Today) >= 3 {
		bestAvg := 1e18
		bestStart := snap.Today[0].Hour
		for i := 0; i <= len(snap.Today)-3; i++ {
			avg := (snap.Today[i].DisplayValue(cur) +
				snap.Today[i+1].DisplayValue(cur) +
				snap.Today[i+2].DisplayValue(cur)) / 3.0
			if avg < bestAvg {
				bestAvg = avg
				bestStart = snap.Today[i].Hour
			}
		}
		snap.CheapestWindowStart = bestStart
	} else {
		snap.CheapestWindowStart = cheapestHr
	}

	return snap
}

func convertDay(raw []apiHour, vatMul, distFee float64, cur string) []HourPrice {
	sorted := append([]apiHour(nil), raw...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Hour < sorted[j].Hour })

	out := make([]HourPrice, len(sorted))
	minV, maxV := 1e18, -1e18
	for i, h := range sorted {
		rawCZK := h.PriceCZK / 1000.0
		finalCZK := rawCZK*vatMul + distFee
		hp := HourPrice{
			Hour:     h.Hour,
			RawCZK:   rawCZK,
			PriceCZK: finalCZK,
			PriceEUR: h.PriceEUR,
			Level:    strings.ToLower(strings.TrimSpace(h.Level)),
			LevelNum: h.LevelNum,
		}
		v := hp.DisplayValue(cur)
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
		out[i] = hp
	}

	span := maxV - minV
	for i := range out {
		if out[i].Level == "low" || out[i].Level == "medium" || out[i].Level == "high" {
			continue
		}
		if span <= 1e-6 {
			out[i].Level = "medium"
			continue
		}
		rel := (out[i].DisplayValue(cur) - minV) / span
		switch {
		case rel < 0.33:
			out[i].Level = "low"
		case rel > 0.67:
			out[i].Level = "high"
		default:
			out[i].Level = "medium"
		}
	}
	return out
}
