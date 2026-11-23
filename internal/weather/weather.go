package weather

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"kindleiot/internal/config"
	"kindleiot/internal/mqtt"
)

// DailyForecast represents one day of forecast data.
type DailyForecast struct {
	Date          string  `json:"date"`
	TempMin       float64 `json:"temp_min"`
	TempMax       float64 `json:"temp_max"`
	WeatherCode   int     `json:"weather_code"`
	Description   string  `json:"description"`
	PrecipProbMax int     `json:"precip_prob_max"`
}

// HourlyForecast represents one hourly forecast slot.
type HourlyForecast struct {
	Time        string  `json:"time"`
	HourLabel   string  `json:"hour_label"`
	Temperature float64 `json:"temperature"`
	PrecipProb  int     `json:"precip_prob"`
	WeatherCode int     `json:"weather_code"`
}

// Snapshot holds the latest fetched weather state.
type Snapshot struct {
	Available    bool             `json:"available"`
	LocationName string           `json:"location_name"`
	Temperature  float64          `json:"temperature"`
	WindSpeed    float64          `json:"wind_speed"`
	WeatherCode  int              `json:"weather_code"`
	Description  string           `json:"description"`
	Icon         string           `json:"icon"`
	Daily        []DailyForecast  `json:"daily"`
	Hourly       []HourlyForecast `json:"hourly"`
	UpdatedAt    time.Time        `json:"updated_at"`
}

// Service periodically fetches weather data from Open-Meteo API (no API key required).
type Service struct {
	mu       sync.RWMutex
	cfg      config.WeatherConfig
	client   *http.Client
	baseURL  string
	snap     Snapshot
	onUpdate func()
	stopCh   chan struct{}
}

// NewService creates a new Open-Meteo weather fetcher.
func NewService(cfg config.WeatherConfig, onUpdate func()) *Service {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs:    mqtt.RootCAPool(),
			MinVersion: tls.VersionTLS12,
		},
	}
	return &Service{
		cfg:     cfg,
		baseURL: "https://api.open-meteo.com/v1/forecast",
		client: &http.Client{
			Transport: tr,
			Timeout:   10 * time.Second,
		},
		onUpdate: onUpdate,
	}
}

// Start launches background periodic weather polling (every 30 minutes).
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
		tick := time.NewTicker(30 * time.Minute)
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

// Stop terminates background weather polling.
func (s *Service) Stop() {
	s.mu.Lock()
	if s.stopCh != nil {
		close(s.stopCh)
		s.stopCh = nil
	}
	s.mu.Unlock()
}

// Reconfigure updates the weather configuration and triggers an immediate refresh if coordinates changed.
func (s *Service) Reconfigure(newCfg config.WeatherConfig) {
	s.mu.Lock()
	changed := s.cfg.Enabled != newCfg.Enabled ||
		s.cfg.Latitude != newCfg.Latitude ||
		s.cfg.Longitude != newCfg.Longitude ||
		s.cfg.LocationName != newCfg.LocationName
	s.cfg = newCfg
	if !newCfg.Enabled {
		s.snap = Snapshot{}
	}
	s.mu.Unlock()

	if changed && newCfg.Enabled {
		go s.FetchNow()
	}
}

// SetEndpoint overrides the Open-Meteo endpoint URL and HTTP client (primarily for testing).
func (s *Service) SetEndpoint(baseURL string, client *http.Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if baseURL != "" {
		s.baseURL = baseURL
	}
	if client != nil {
		s.client = client
	}
}

// Snapshot returns the latest cached weather forecast.
func (s *Service) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := s.snap
	if len(s.snap.Daily) > 0 {
		out.Daily = append([]DailyForecast(nil), s.snap.Daily...)
	}
	if len(s.snap.Hourly) > 0 {
		out.Hourly = append([]HourlyForecast(nil), s.snap.Hourly...)
	}
	return out
}

type openMeteoResponse struct {
	Current struct {
		Temperature float64 `json:"temperature_2m"`
		WeatherCode int     `json:"weather_code"`
		WindSpeed   float64 `json:"wind_speed_10m"`
	} `json:"current"`
	Hourly struct {
		Time        []string  `json:"time"`
		Temperature []float64 `json:"temperature_2m"`
		PrecipProb  []int     `json:"precipitation_probability"`
		WeatherCode []int     `json:"weather_code"`
	} `json:"hourly"`
	Daily struct {
		Time          []string  `json:"time"`
		WeatherCode   []int     `json:"weather_code"`
		TempMax       []float64 `json:"temperature_2m_max"`
		TempMin       []float64 `json:"temperature_2m_min"`
		PrecipProbMax []int     `json:"precipitation_probability_max"`
	} `json:"daily"`
}

// FetchNow performs a synchronous weather update from Open-Meteo.
func (s *Service) FetchNow() {
	s.mu.RLock()
	cfg := s.cfg
	baseURL := s.baseURL
	client := s.client
	s.mu.RUnlock()

	if !cfg.Enabled {
		return
	}

	url := fmt.Sprintf("%s?latitude=%.4f&longitude=%.4f&current=temperature_2m,weather_code,wind_speed_10m&hourly=temperature_2m,precipitation_probability,weather_code&daily=weather_code,temperature_2m_max,temperature_2m_min,precipitation_probability_max&timezone=auto&forecast_days=7",
		baseURL, cfg.Latitude, cfg.Longitude)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}

	var parsed openMeteoResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return
	}

	desc, icon := DescribeWMO(parsed.Current.WeatherCode)
	loc := cfg.LocationName
	if loc == "" {
		loc = "Počasí"
	}

	var daily []DailyForecast
	n := len(parsed.Daily.Time)
	if len(parsed.Daily.TempMin) < n {
		n = len(parsed.Daily.TempMin)
	}
	if len(parsed.Daily.TempMax) < n {
		n = len(parsed.Daily.TempMax)
	}
	if len(parsed.Daily.WeatherCode) < n {
		n = len(parsed.Daily.WeatherCode)
	}
	for i := 0; i < n && i < 7; i++ {
		dDesc, _ := DescribeWMO(parsed.Daily.WeatherCode[i])
		pmax := 0
		if i < len(parsed.Daily.PrecipProbMax) {
			pmax = parsed.Daily.PrecipProbMax[i]
		}
		daily = append(daily, DailyForecast{
			Date:          parsed.Daily.Time[i],
			TempMin:       parsed.Daily.TempMin[i],
			TempMax:       parsed.Daily.TempMax[i],
			WeatherCode:   parsed.Daily.WeatherCode[i],
			Description:   dDesc,
			PrecipProbMax: pmax,
		})
	}

	var hourly []HourlyForecast
	hn := len(parsed.Hourly.Time)
	if len(parsed.Hourly.Temperature) < hn {
		hn = len(parsed.Hourly.Temperature)
	}
	startIdx := 0
	nowHour := time.Now().Format("2006-01-02T15:00")
	for i := 0; i < hn; i++ {
		if parsed.Hourly.Time[i] >= nowHour {
			startIdx = i
			break
		}
	}
	for i := startIdx; i < hn && len(hourly) < 24; i++ {
		ts := parsed.Hourly.Time[i]
		lbl := ts
		if ti := strings.IndexByte(ts, 'T'); ti >= 0 && ti+1 < len(ts) {
			lbl = ts[ti+1:]
		}
		pp := 0
		if i < len(parsed.Hourly.PrecipProb) {
			pp = parsed.Hourly.PrecipProb[i]
		}
		wc := 0
		if i < len(parsed.Hourly.WeatherCode) {
			wc = parsed.Hourly.WeatherCode[i]
		}
		hourly = append(hourly, HourlyForecast{
			Time:        ts,
			HourLabel:   lbl,
			Temperature: parsed.Hourly.Temperature[i],
			PrecipProb:  pp,
			WeatherCode: wc,
		})
	}

	snap := Snapshot{
		Available:    true,
		LocationName: loc,
		Temperature:  parsed.Current.Temperature,
		WindSpeed:    parsed.Current.WindSpeed,
		WeatherCode:  parsed.Current.WeatherCode,
		Description:  desc,
		Icon:         icon,
		Daily:        daily,
		Hourly:       hourly,
		UpdatedAt:    time.Now(),
	}

	s.mu.Lock()
	s.snap = snap
	cb := s.onUpdate
	s.mu.Unlock()

	if cb != nil {
		cb()
	}
}

// DescribeWMO translates a WMO weather code into a Czech description and vector icon name.
func DescribeWMO(code int) (desc string, icon string) {
	switch {
	case code == 0:
		return "Jasno", "sun"
	case code == 1 || code == 2:
		return "Polojasno", "sun"
	case code == 3:
		return "Zataženo", "fan"
	case code == 45 || code == 48:
		return "Mlha", "fan"
	case code >= 51 && code <= 67:
		return "Déšť", "drop"
	case code >= 71 && code <= 77:
		return "Sněžení", "drop"
	case code >= 80 && code <= 82:
		return "Přeháňky", "drop"
	case code >= 85 && code <= 86:
		return "Sněhové přeháňky", "drop"
	case code >= 95:
		return "Bouřka", "bolt"
	default:
		return "Proměnlivo", "sun"
	}
}
