package web

import (
	"bytes"
	"encoding/json"
	"image"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"kindleiot/internal/config"
	"kindleiot/internal/kindle"
	"kindleiot/internal/mqtt"
)

func TestWebEndpoints(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := config.DefaultConfig()
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("Save config failed: %v", err)
	}

	mq := mqtt.NewManager(cfg, nil, mqtt.CommandHandler{})
	hw := kindle.NewSystem()
	img := image.NewGray(image.Rect(0, 0, 100, 100))

	var tappedX, tappedY int
	srv := NewServer(cfgPath, cfg, mq, hw, Hooks{
		GetImage: func() *image.Gray { return img },
		HandleTap: func(x, y int) {
			tappedX, tappedY = x, y
		},
	})
	h := srv.Handler()

	// GET /api/screen.png
	req := httptest.NewRequest(http.MethodGet, "/api/screen.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("unexpected /api/screen.png response: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}

	// POST /api/tap
	tapBody, _ := json.Marshal(map[string]int{"x": 123, "y": 456})
	req = httptest.NewRequest(http.MethodPost, "/api/tap", bytes.NewReader(tapBody))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || tappedX != 123 || tappedY != 456 {
		t.Fatalf("unexpected tap result: code=%d got=(%d,%d)", rec.Code, tappedX, tappedY)
	}

	// GET /api/config
	req = httptest.NewRequest(http.MethodGet, "/api/config", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		t.Fatalf("unexpected GET /api/config: %d", rec.Code)
	}

	// POST /api/config invalid YAML rejected
	badBody, _ := json.Marshal(map[string]string{"yaml": "mqtt:\n  broker: ''\n"})
	req = httptest.NewRequest(http.MethodPost, "/api/config", bytes.NewReader(badBody))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on invalid config, got %d", rec.Code)
	}

	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("config file missing: %v", err)
	}

	// GET /api/config/json
	req = httptest.NewRequest(http.MethodGet, "/api/config/json", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected GET /api/config/json: %d", rec.Code)
	}
	var gotCfg config.Config
	if err := json.Unmarshal(rec.Body.Bytes(), &gotCfg); err != nil {
		t.Fatalf("unmarshal /api/config/json failed: %v", err)
	}
	gotCfg.Device.Title = "Updated Panel"
	rawJSON, _ := json.Marshal(gotCfg)

	// POST /api/config/json
	req = httptest.NewRequest(http.MethodPost, "/api/config/json", bytes.NewReader(rawJSON))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected POST /api/config/json: %d (%s)", rec.Code, rec.Body.String())
	}
	reloaded, err := config.Load(cfgPath)
	if err != nil || reloaded.Device.Title != "Updated Panel" {
		t.Fatalf("expected updated config saved to yaml, got err=%v title=%q", err, reloaded.Device.Title)
	}
}
