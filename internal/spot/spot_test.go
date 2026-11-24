package spot

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"kindleiot/internal/config"
)

func almostEq(a, b float64) bool {
	return math.Abs(a-b) < 0.01
}

func TestSpotPriceFetchAndCalculations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"hoursToday": [
				{"hour": 0, "priceEur": 100.0, "priceCZK": 2500, "level": "low", "levelNum": 2},
				{"hour": 1, "priceEur": 80.0,  "priceCZK": 2000, "level": "low", "levelNum": 1},
				{"hour": 2, "priceEur": 120.0, "priceCZK": 3000, "level": "medium", "levelNum": 10},
				{"hour": 18,"priceEur": 240.0, "priceCZK": 6000, "level": "high", "levelNum": 24}
			],
			"hoursTomorrow": [
				{"hour": 0, "priceEur": 90.0, "priceCZK": 2250, "level": "", "levelNum": 0},
				{"hour": 1, "priceEur": 180.0,"priceCZK": 4500, "level": "", "levelNum": 0}
			]
		}`)
	}))
	defer srv.Close()

	var (
		mu       sync.Mutex
		notified int
	)
	svc := NewService(config.SpotPriceConfig{
		Enabled:         true,
		Currency:        "CZK",
		VATPercent:      config.Float64Ptr(21),
		DistributionFee: 1.5,
	}, func(s Snapshot) {
		mu.Lock()
		notified++
		mu.Unlock()
	})
	svc.SetBaseURL(srv.URL)
	svc.SetHTTPClient(srv.Client())
	svc.FetchNow()

	mu.Lock()
	n := notified
	mu.Unlock()
	if n == 0 {
		t.Fatalf("expected onUpdate callback after FetchNow")
	}

	snap := svc.SnapshotAt(time.Date(2026, 11, 24, 18, 15, 0, 0, time.Local))
	if !snap.Available || len(snap.Today) != 4 || len(snap.Tomorrow) != 2 {
		t.Fatalf("unexpected snapshot counts: %+v", snap)
	}
	if snap.Current == nil || snap.Current.Hour != 18 {
		t.Fatalf("expected current hour 18, got %+v", snap.Current)
	}
	// 6000 CZK/MWh = 6.00 Kč/kWh * 1.21 + 1.50 = 8.76 Kč/kWh
	if !almostEq(snap.Current.PriceCZK, 8.76) {
		t.Fatalf("expected current PriceCZK=8.76, got %.4f", snap.Current.PriceCZK)
	}
	// Cheapest hour is hour 1: 2.00 * 1.21 + 1.50 = 3.92 Kč/kWh
	if snap.CheapestHour != 1 || !almostEq(snap.MinToday, 3.92) {
		t.Fatalf("expected cheapest hour 1 (3.92), got h=%d min=%.4f", snap.CheapestHour, snap.MinToday)
	}
	if snap.Tomorrow[0].Level != "low" || snap.Tomorrow[1].Level != "high" {
		t.Fatalf("expected fallback level classification on tomorrow prices, got %+v", snap.Tomorrow)
	}

	// Reconfigure to EUR without refetching updates displayed values cleanly.
	svc.Reconfigure(config.SpotPriceConfig{
		Enabled:    true,
		Currency:   "EUR",
		VATPercent: config.Float64Ptr(0),
	})
	eurSnap := svc.SnapshotAt(time.Date(2026, 11, 24, 1, 0, 0, 0, time.Local))
	if eurSnap.Unit != "€/MWh" || !almostEq(eurSnap.Current.DisplayValue(eurSnap.Currency), 80.0) {
		t.Fatalf("expected EUR mode at hour 1 = 80 EUR/MWh, got unit=%q val=%v", eurSnap.Unit, eurSnap.Current)
	}
}
