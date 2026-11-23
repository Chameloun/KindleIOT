package weather

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"kindleiot/internal/config"
)

func TestWeatherServiceFetchNow(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"current": {"temperature_2m": 14.3, "weather_code": 1, "wind_speed_10m": 12.5},
			"hourly": {
				"time": ["2026-11-23T11:00", "2026-11-23T12:00", "2026-11-23T13:00", "2026-11-23T14:00"],
				"temperature_2m": [13.5, 14.3, 14.9, 14.2],
				"precipitation_probability": [10, 15, 25, 40],
				"weather_code": [1, 1, 2, 61]
			},
			"daily": {
				"time": ["2026-11-23", "2026-11-24", "2026-11-25", "2026-11-26", "2026-11-27", "2026-11-28", "2026-11-29"],
				"weather_code": [1, 61, 0, 2, 3, 0, 1],
				"temperature_2m_max": [15.0, 11.2, 13.8, 12.0, 10.5, 11.0, 12.5],
				"temperature_2m_min": [6.1, 4.0, 3.5, 2.0, 1.5, 2.5, 3.0],
				"precipitation_probability_max": [20, 80, 5, 15, 40, 10, 10]
			}
		}`))
	}))
	defer ts.Close()

	svc := NewService(config.WeatherConfig{
		Enabled:      true,
		LocationName: "Praha",
		Latitude:     50.0755,
		Longitude:    14.4378,
	}, nil)
	svc.SetEndpoint(ts.URL, ts.Client())

	svc.FetchNow()
	snap := svc.Snapshot()
	if !snap.Available || snap.LocationName != "Praha" || snap.Temperature != 14.3 || snap.Description != "Polojasno" {
		t.Fatalf("unexpected weather snapshot: %+v", snap)
	}
	if len(snap.Daily) != 7 || snap.Daily[1].Description != "Déšť" || snap.Daily[1].PrecipProbMax != 80 {
		t.Fatalf("unexpected 7-day forecast: %+v", snap.Daily)
	}
	if len(snap.Hourly) == 0 || snap.Hourly[len(snap.Hourly)-1].PrecipProb != 40 {
		t.Fatalf("unexpected hourly forecast: %+v", snap.Hourly)
	}
	if enDesc, _ := DescribeWMOLang(61, "en"); enDesc != "Rain" {
		t.Fatalf("expected English WMO description Rain, got %q", enDesc)
	}
	if csDesc, _ := DescribeWMOLang(95, "cs"); csDesc != "Bouřka" {
		t.Fatalf("expected Czech WMO description Bouřka, got %q", csDesc)
	}
}
