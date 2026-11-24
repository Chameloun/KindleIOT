package web

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kindleiot/internal/ble"
	"kindleiot/internal/checklist"
	"kindleiot/internal/config"
	"kindleiot/internal/kindle"
	"kindleiot/internal/mqtt"
	"kindleiot/internal/rules"
	"kindleiot/internal/spot"
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

	bleSvc := ble.NewService(cfg.Bluetooth, false, ble.Callbacks{})

	var tappedX, tappedY int
	srv := NewServer(cfgPath, cfg, mq, hw, Hooks{
		GetImage: func() *image.Gray { return img },
		HandleTap: func(x, y int) {
			tappedX, tappedY = x, y
		},
		BLEStatus: func() ble.Status {
			return bleSvc.Status()
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
	gotCfg.Device.Language = "cs"
	rawJSON, _ := json.Marshal(gotCfg)

	// POST /api/config/json
	req = httptest.NewRequest(http.MethodPost, "/api/config/json", bytes.NewReader(rawJSON))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected POST /api/config/json: %d (%s)", rec.Code, rec.Body.String())
	}
	reloaded, err := config.Load(cfgPath)
	if err != nil || reloaded.Device.Title != "Updated Panel" || reloaded.Device.Language != "cs" {
		t.Fatalf("expected updated config saved to yaml, got err=%v title=%q lang=%q", err, reloaded.Device.Title, reloaded.Device.Language)
	}

	// GET /i18n.js serves embedded localization bundle
	req = httptest.NewRequest(http.MethodGet, "/i18n.js", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "en:") || !strings.Contains(body, "cs:") {
		t.Fatalf("expected /i18n.js HTTP 200 with en/cs dicts, got code=%d len=%d", rec.Code, len(body))
	}

	// GET /api/status includes "ble"
	req = httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected GET /api/status: %d", rec.Code)
	}
	var stBody map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &stBody); err != nil {
		t.Fatalf("unmarshal /api/status failed: %v", err)
	}
	if _, ok := stBody["ble"]; !ok {
		t.Fatalf("expected /api/status to include ble key")
	}

	// GET /api/ble/status returns 200 JSON ble.Status
	req = httptest.NewRequest(http.MethodGet, "/api/ble/status", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected GET /api/ble/status: %d", rec.Code)
	}
	var bleGot ble.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &bleGot); err != nil {
		t.Fatalf("unmarshal /api/ble/status failed: %v", err)
	}
	if bleGot.LastSeenSec != -1 {
		t.Fatalf("expected initial LastSeenSec=-1, got %+v", bleGot)
	}
}

func makeFakeELF(class byte, machine uint16, size int) []byte {
	if size < 64 {
		size = 64
	}
	b := make([]byte, size)
	b[0], b[1], b[2], b[3] = 0x7f, 'E', 'L', 'F'
	b[4] = class
	b[5] = 1 // little-endian
	binary.LittleEndian.PutUint16(b[18:20], machine)
	return b
}

func TestOTAExportImportAndShortcutsAPI(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := config.DefaultConfig()
	cfg.Pages = append(cfg.Pages, config.PageConfig{
		ID:      "notes",
		Title:   "Notes",
		Columns: 2,
		Widgets: []config.WidgetConfig{
			{ID: "chk1", Type: "checklist", Label: "To-Do"},
		},
	})
	_ = config.Save(cfgPath, cfg)

	mq := mqtt.NewManager(cfg, nil, mqtt.CommandHandler{})
	hw := kindle.NewSystem()
	chkStore := checklist.NewStore(filepath.Join(dir, "checklists.json"), nil)

	var (
		otaBytes    int
		reloadedCfg *config.Config
		gotNotify   string
		gotPage     string
		standby     bool
		gotBright   = -1
		gotTrig     string
		gotImgRef   string
	)

	srv := NewServer(cfgPath, cfg, mq, hw, Hooks{
		ExpectedArch: "arm",
		MinOTASize:   64,
		ApplyOTA: func(data []byte) error {
			otaBytes = len(data)
			return nil
		},
		ReloadConfig: func(newCfg *config.Config) { reloadedCfg = newCfg },
		Notify: func(text string, _ time.Duration) { gotNotify = text },
		SwitchPage: func(sel string) (string, string, bool) {
			if sel == "notes" {
				gotPage = sel
				return "notes", "Notes", true
			}
			return "", "", false
		},
		SetStandby: func(mode string) bool {
			if mode == "true" {
				standby = true
			} else if mode == "false" {
				standby = false
			} else {
				standby = !standby
			}
			return standby
		},
		SetBrightness:  func(lvl int) { gotBright = lvl },
		TriggerWidget:  func(wid, st string) bool { gotTrig = wid + ":" + st; return true },
		ChecklistStore: chkStore,
		SpotStatus: func() spot.Snapshot {
			return spot.Snapshot{Available: true, Enabled: true, Currency: "CZK", Unit: "Kč/kWh"}
		},
		BatteryGuardStatus: func() rules.BatteryGuardStatus {
			return rules.BatteryGuardStatus{Enabled: true, State: "ok", MinPercent: 30, MaxPercent: 80}
		},
		RefreshImage: func(wid string) { gotImgRef = wid },
	})
	h := srv.Handler()

	// 1. Reject non-ELF OTA upload
	req := httptest.NewRequest(http.MethodPost, "/api/ota/upload", strings.NewReader(strings.Repeat("zip", 40)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-ELF OTA, got %d", rec.Code)
	}

	// 2. Reject wrong arch (amd64 ELF64) when ExpectedArch=arm
	amd64ELF := makeFakeELF(2, 62, 128)
	req = httptest.NewRequest(http.MethodPost, "/api/ota/upload", bytes.NewReader(amd64ELF))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for wrong-arch ELF OTA, got %d", rec.Code)
	}

	// 3. Accept valid ARM ELF32 multipart upload
	armELF := makeFakeELF(1, 40, 256)
	var mpBuf bytes.Buffer
	mw := multipart.NewWriter(&mpBuf)
	fw, _ := mw.CreateFormFile("file", "kindleiot-arm")
	_, _ = fw.Write(armELF)
	_ = mw.Close()
	req = httptest.NewRequest(http.MethodPost, "/api/ota/upload", &mpBuf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || otaBytes != 256 {
		t.Fatalf("expected 200 valid ARM OTA upload (256 bytes), got code=%d bytes=%d body=%s", rec.Code, otaBytes, rec.Body.String())
	}

	// 4. Config export & import YAML
	req = httptest.NewRequest(http.MethodGet, "/api/config/export", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Disposition"), "kindleiot-config.yaml") {
		t.Fatalf("unexpected export response: code=%d disp=%q", rec.Code, rec.Header().Get("Content-Disposition"))
	}
	importedYAML := strings.Replace(rec.Body.String(), "IoT Panel", "Imported Home", 1)
	req = httptest.NewRequest(http.MethodPost, "/api/config/import", strings.NewReader(importedYAML))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || reloadedCfg == nil || reloadedCfg.Device.Title != "Imported Home" {
		t.Fatalf("import failed: code=%d cfg=%v body=%s", rec.Code, reloadedCfg, rec.Body.String())
	}

	// 5. Shortcuts API endpoints
	req = httptest.NewRequest(http.MethodPost, "/api/notify", strings.NewReader(`{"text":"Washing done!","duration_sec":30}`))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || gotNotify != "Washing done!" {
		t.Fatalf("/api/notify failed: code=%d got=%q", rec.Code, gotNotify)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/page?page=notes", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || gotPage != "notes" {
		t.Fatalf("/api/page failed: code=%d got=%q", rec.Code, gotPage)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/standby", strings.NewReader(`{"active":true}`))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !standby {
		t.Fatalf("/api/standby failed: code=%d standby=%v", rec.Code, standby)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/brightness", strings.NewReader(`{"level":19}`))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || gotBright != 19 {
		t.Fatalf("/api/brightness failed: code=%d got=%d", rec.Code, gotBright)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/checklist", strings.NewReader(`{"action":"add","text":"Buy coffee"}`))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || len(chkStore.Items("chk1")) != 1 {
		t.Fatalf("/api/checklist add failed: code=%d items=%+v body=%s", rec.Code, chkStore.Items("chk1"), rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/widget/trigger", strings.NewReader(`{"widget_id":"living_lamp","state":"ON"}`))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || gotTrig != "living_lamp:ON" {
		t.Fatalf("/api/widget/trigger failed: code=%d got=%q", rec.Code, gotTrig)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/spot", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "CZK") {
		t.Fatalf("/api/spot failed: code=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/image/refresh?widget_id=radar1", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || gotImgRef != "radar1" {
		t.Fatalf("/api/image/refresh failed: code=%d got=%q", rec.Code, gotImgRef)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var fullSt map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &fullSt)
	for _, k := range []string{"battery_guard", "spot", "checklists", "pages", "arch"} {
		if _, ok := fullSt[k]; !ok {
			t.Fatalf("expected %q key in /api/status", k)
		}
	}
}
