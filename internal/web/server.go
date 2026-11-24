package web

import (
	"bytes"
	"embed"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"io/fs"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"kindleiot/internal/ble"
	"kindleiot/internal/checklist"
	"kindleiot/internal/config"
	"kindleiot/internal/homekit"
	"kindleiot/internal/kindle"
	"kindleiot/internal/mqtt"
	"kindleiot/internal/rules"
	"kindleiot/internal/spot"
)

//go:embed static/*
var staticFS embed.FS

// Hooks connect web HTTP actions back to the running renderer, services, and MQTT client.
type Hooks struct {
	GetImage           func() *image.Gray
	HandleTap          func(x, y int)
	ReloadConfig       func(newCfg *config.Config)
	HomeKitStatus      func() homekit.Status
	ResetHomeKit       func() error
	BLEStatus          func() ble.Status
	ApplyOTA           func(data []byte) error
	ExpectedArch       string
	MinOTASize         int
	Notify             func(text string, dur time.Duration)
	SwitchPage         func(selector string) (string, string, bool)
	SetStandby         func(mode string) bool
	SetBrightness      func(level int)
	TriggerWidget      func(widgetID, state string) bool
	ChecklistStore     *checklist.Store
	SpotStatus         func() spot.Snapshot
	BatteryGuardStatus func() rules.BatteryGuardStatus
	RulesLog           func() []rules.ExecutionLog
	RefreshImage       func(widgetID string)
}

// Server serves the live screen mirror, visual drag-and-drop builder, OTA updater, Shortcuts REST API, and YAML editor.
type Server struct {
	mu         sync.RWMutex
	configPath string
	cfg        *config.Config
	mq         *mqtt.Manager
	hw         *kindle.System
	hooks      Hooks
}

// NewServer creates a web management server instance.
func NewServer(configPath string, cfg *config.Config, mq *mqtt.Manager, hw *kindle.System, hooks Hooks) *Server {
	return &Server{
		configPath: configPath,
		cfg:        cfg,
		mq:         mq,
		hw:         hw,
		hooks:      hooks,
	}
}

// Handler returns the HTTP handler mux for testing or http.Server.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(staticFS, "static")
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/api/screen.png", s.handleScreenPNG)
	mux.HandleFunc("/api/tap", s.handleTap)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/config/json", s.handleConfigJSON)
	mux.HandleFunc("/api/config/export", s.handleConfigExport)
	mux.HandleFunc("/api/config/import", s.handleConfigImport)
	mux.HandleFunc("/api/homekit/reset", s.handleHomeKitReset)
	mux.HandleFunc("/api/ble/status", s.handleBLEStatus)
	mux.HandleFunc("/api/ota/upload", s.handleOTAUpload)
	mux.HandleFunc("/api/notify", s.handleNotify)
	mux.HandleFunc("/api/page", s.handlePageSwitch)
	mux.HandleFunc("/api/standby", s.handleStandby)
	mux.HandleFunc("/api/brightness", s.handleBrightness)
	mux.HandleFunc("/api/checklist", s.handleChecklist)
	mux.HandleFunc("/api/widget/trigger", s.handleWidgetTrigger)
	mux.HandleFunc("/api/spot", s.handleSpotStatus)
	mux.HandleFunc("/api/image/refresh", s.handleImageRefresh)
	return mux
}

func (s *Server) handleBLEStatus(w http.ResponseWriter, r *http.Request) {
	var st ble.Status
	if s.hooks.BLEStatus != nil {
		st = s.hooks.BLEStatus()
	} else {
		st.LastSeenSec = -1
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleHomeKitReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if s.hooks.ResetHomeKit != nil {
		if err := s.hooks.ResetHomeKit(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleScreenPNG(w http.ResponseWriter, r *http.Request) {
	if s.hooks.GetImage == nil {
		http.Error(w, "screen unavailable", http.StatusServiceUnavailable)
		return
	}
	img := s.hooks.GetImage()
	if img == nil {
		http.Error(w, "no frame rendered yet", http.StatusServiceUnavailable)
		return
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) handleTap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		X int `json:"x"`
		Y int `json:"y"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if s.hooks.HandleTap != nil {
		s.hooks.HandleTap(body.X, body.Y)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	snap := s.mq.Snapshot()
	hwStat := s.hw.ReadStatus()
	mergedTopics := make(map[string]mqtt.TopicValue)
	for _, b := range snap.Brokers {
		for k, v := range b.Topics {
			mergedTopics[k] = v
		}
	}
	var hkStat homekit.Status
	if s.hooks.HomeKitStatus != nil {
		hkStat = s.hooks.HomeKitStatus()
	}
	var bleStat ble.Status
	if s.hooks.BLEStatus != nil {
		bleStat = s.hooks.BLEStatus()
	} else {
		bleStat.LastSeenSec = -1
	}

	var spotStat spot.Snapshot
	if s.hooks.SpotStatus != nil {
		spotStat = s.hooks.SpotStatus()
	}
	var batGuard rules.BatteryGuardStatus
	if s.hooks.BatteryGuardStatus != nil {
		batGuard = s.hooks.BatteryGuardStatus()
	} else {
		batGuard.State = "disabled"
	}
	var rulesLog []rules.ExecutionLog
	if s.hooks.RulesLog != nil {
		rulesLog = s.hooks.RulesLog()
	}
	checklists := map[string][]config.ChecklistItem{}
	if s.hooks.ChecklistStore != nil {
		checklists = s.hooks.ChecklistStore.All()
	}

	s.mu.RLock()
	var pageSummaries []map[string]any
	if s.cfg != nil {
		for _, p := range s.cfg.Pages {
			pageSummaries = append(pageSummaries, map[string]any{
				"id":     p.ID,
				"title":  p.Title,
				"hidden": p.Hidden,
			})
		}
	}
	s.mu.RUnlock()

	resp := map[string]any{
		"arch":             runtime.GOARCH,
		"mqtt_connected":   snap.AllConnected,
		"mqtt_any":         snap.AnyConnected,
		"mqtt_connected_n": snap.ConnectedCount,
		"mqtt_total_n":     snap.TotalCount,
		"mqtt_error":       snap.FirstError,
		"brokers":          snap.Brokers,
		"topics":           mergedTopics,
		"device":           hwStat,
		"homekit":          hkStat,
		"ble":              bleStat,
		"spot":             spotStat,
		"battery_guard":    batGuard,
		"rules_log":        rulesLog,
		"checklists":       checklists,
		"pages":            pageSummaries,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		raw, err := os.ReadFile(s.configPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
		_, _ = w.Write(raw)

	case http.MethodPost:
		var body struct {
			YAML string `json:"yaml"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		newCfg, err := config.Parse([]byte(body.YAML))
		if err != nil {
			http.Error(w, "invalid config: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := os.WriteFile(s.configPath, []byte(body.YAML), 0o644); err != nil {
			http.Error(w, "save failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		s.applyConfig(newCfg)
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleConfigJSON(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, err := config.Load(s.configPath)
		if err != nil {
			s.mu.RLock()
			cfg = s.cfg
			s.mu.RUnlock()
		}
		writeJSON(w, http.StatusOK, cfg)

	case http.MethodPost:
		var newCfg config.Config
		if err := json.NewDecoder(r.Body).Decode(&newCfg); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := config.Save(s.configPath, &newCfg); err != nil {
			http.Error(w, "invalid config: "+err.Error(), http.StatusBadRequest)
			return
		}
		s.applyConfig(&newCfg)
		writeJSON(w, http.StatusOK, &newCfg)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleConfigExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	raw, err := os.ReadFile(s.configPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="kindleiot-config.yaml"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
}

func (s *Server) handleConfigImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	var raw []byte
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			http.Error(w, "invalid multipart upload: "+err.Error(), http.StatusBadRequest)
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "missing form file 'file': "+err.Error(), http.StatusBadRequest)
			return
		}
		defer file.Close()
		raw, err = io.ReadAll(io.LimitReader(file, 4<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		var err error
		raw, err = io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		http.Error(w, "empty config payload", http.StatusBadRequest)
		return
	}

	var newCfg *config.Config
	if trimmed[0] == '{' {
		var wrapper struct {
			YAML string `json:"yaml"`
		}
		if err := json.Unmarshal(trimmed, &wrapper); err == nil && strings.TrimSpace(wrapper.YAML) != "" {
			parsed, perr := config.Parse([]byte(wrapper.YAML))
			if perr != nil {
				http.Error(w, "invalid YAML: "+perr.Error(), http.StatusBadRequest)
				return
			}
			newCfg = parsed
		} else {
			var parsed config.Config
			if err := json.Unmarshal(trimmed, &parsed); err != nil {
				http.Error(w, "invalid JSON config: "+err.Error(), http.StatusBadRequest)
				return
			}
			newCfg = &parsed
		}
	} else {
		parsed, err := config.Parse(raw)
		if err != nil {
			http.Error(w, "invalid YAML config: "+err.Error(), http.StatusBadRequest)
			return
		}
		newCfg = parsed
	}

	if err := config.Save(s.configPath, newCfg); err != nil {
		http.Error(w, "invalid config: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.applyConfig(newCfg)
	writeJSON(w, http.StatusOK, newCfg)
}

func (s *Server) handleOTAUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 42<<20)

	var data []byte
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(40 << 20); err != nil {
			http.Error(w, "upload failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			file, _, err = r.FormFile("binary")
			if err != nil {
				http.Error(w, "missing form file 'file'", http.StatusBadRequest)
				return
			}
		}
		defer file.Close()
		data, err = io.ReadAll(io.LimitReader(file, 40<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		var err error
		data, err = io.ReadAll(io.LimitReader(r.Body, 40<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	if err := ValidateOTABinary(data, s.hooks.ExpectedArch, s.hooks.MinOTASize); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if s.hooks.ApplyOTA == nil {
		http.Error(w, "OTA handler not configured", http.StatusServiceUnavailable)
		return
	}
	if err := s.hooks.ApplyOTA(data); err != nil {
		http.Error(w, "OTA install failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"bytes":      len(data),
		"restarting": true,
	})
}

func (s *Server) handleNotify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
		return
	}
	params := parseFlexParams(r)
	text := strings.TrimSpace(paramStr(r, params, "text"))
	if text == "" {
		http.Error(w, "missing 'text'", http.StatusBadRequest)
		return
	}
	durSec := 15
	if ds := paramStr(r, params, "duration_sec"); ds != "" {
		if n, err := strconv.Atoi(ds); err == nil {
			if n < 1 {
				n = 1
			} else if n > 3600 {
				n = 3600
			}
			durSec = n
		}
	}
	if s.hooks.Notify != nil {
		s.hooks.Notify(text, time.Duration(durSec)*time.Second)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"text":         text,
		"duration_sec": durSec,
	})
}

func (s *Server) handlePageSwitch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
		return
	}
	params := parseFlexParams(r)
	page := strings.TrimSpace(paramStr(r, params, "page"))
	if page == "" {
		http.Error(w, "missing 'page'", http.StatusBadRequest)
		return
	}
	if s.hooks.SwitchPage == nil {
		http.Error(w, "page switcher unavailable", http.StatusServiceUnavailable)
		return
	}
	pid, ptitle, ok := s.hooks.SwitchPage(page)
	if !ok {
		http.Error(w, "page not found or hidden", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"page_id":    pid,
		"page_title": ptitle,
	})
}

func (s *Server) handleStandby(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
		return
	}
	params := parseFlexParams(r)
	active := strings.TrimSpace(paramStr(r, params, "active"))
	if active == "" {
		active = "toggle"
	}
	if s.hooks.SetStandby == nil {
		http.Error(w, "standby control unavailable", http.StatusServiceUnavailable)
		return
	}
	res := s.hooks.SetStandby(active)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"standby": res,
	})
}

func (s *Server) handleBrightness(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
		return
	}
	params := parseFlexParams(r)
	raw := strings.TrimSpace(paramStr(r, params, "level"))
	if raw == "" {
		http.Error(w, "missing 'level'", http.StatusBadRequest)
		return
	}
	lvl, err := strconv.Atoi(raw)
	if err != nil {
		http.Error(w, "invalid integer 'level'", http.StatusBadRequest)
		return
	}
	if lvl < 0 {
		lvl = 0
	} else if lvl > 24 {
		lvl = 24
	}
	if s.hooks.SetBrightness != nil {
		s.hooks.SetBrightness(lvl)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"level": lvl,
	})
}

func (s *Server) handleWidgetTrigger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
		return
	}
	params := parseFlexParams(r)
	wid := strings.TrimSpace(paramStr(r, params, "widget_id"))
	if wid == "" {
		http.Error(w, "missing 'widget_id'", http.StatusBadRequest)
		return
	}
	state := strings.TrimSpace(paramStr(r, params, "state"))
	if state == "" {
		state = "TOGGLE"
	}
	if s.hooks.TriggerWidget == nil {
		http.Error(w, "widget trigger unavailable", http.StatusServiceUnavailable)
		return
	}
	if !s.hooks.TriggerWidget(wid, state) {
		http.Error(w, "widget not found or not triggerable", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"widget_id": wid,
		"state":     strings.ToUpper(state),
	})
}

func (s *Server) handleChecklist(w http.ResponseWriter, r *http.Request) {
	if s.hooks.ChecklistStore == nil {
		http.Error(w, "checklist store unavailable", http.StatusServiceUnavailable)
		return
	}

	s.mu.RLock()
	curCfg := s.cfg
	s.mu.RUnlock()

	resolveWid := func(raw string) string {
		wid := strings.TrimSpace(raw)
		if wid == "" {
			wid = checklist.FirstWidgetID(curCfg)
		}
		return wid
	}

	switch r.Method {
	case http.MethodGet:
		wid := resolveWid(r.URL.Query().Get("widget_id"))
		var items []config.ChecklistItem
		if wid != "" {
			items = s.hooks.ChecklistStore.Items(wid)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"widget_id":  wid,
			"items":      items,
			"checklists": s.hooks.ChecklistStore.All(),
		})

	case http.MethodPost:
		params := parseFlexParams(r)
		wid := resolveWid(paramStr(r, params, "widget_id"))
		if wid == "" {
			http.Error(w, "missing 'widget_id' (no checklist widget configured)", http.StatusBadRequest)
			return
		}
		action := strings.ToLower(strings.TrimSpace(paramStr(r, params, "action")))
		if action == "" {
			action = "add"
		}
		switch action {
		case "add":
			text := paramStr(r, params, "text")
			if _, err := s.hooks.ChecklistStore.Add(wid, text); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		case "toggle":
			itemID := strings.TrimSpace(paramStr(r, params, "item_id"))
			if _, ok := s.hooks.ChecklistStore.Toggle(wid, itemID); !ok {
				http.Error(w, "item not found", http.StatusNotFound)
				return
			}
		case "delete":
			itemID := strings.TrimSpace(paramStr(r, params, "item_id"))
			if !s.hooks.ChecklistStore.Delete(wid, itemID) {
				http.Error(w, "item not found", http.StatusNotFound)
				return
			}
		case "clear_done":
			s.hooks.ChecklistStore.ClearDone(wid)
		case "replace":
			rawItems, hasItems := params["items"]
			if !hasItems {
				http.Error(w, "missing 'items'", http.StatusBadRequest)
				return
			}
			b, _ := json.Marshal(rawItems)
			var parsed []config.ChecklistItem
			if err := json.Unmarshal(b, &parsed); err != nil {
				http.Error(w, "invalid 'items': "+err.Error(), http.StatusBadRequest)
				return
			}
			if err := s.hooks.ChecklistStore.Replace(wid, parsed); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		default:
			http.Error(w, "unsupported checklist action "+action, http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":        true,
			"widget_id": wid,
			"items":     s.hooks.ChecklistStore.Items(wid),
		})

	default:
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleSpotStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	var st spot.Snapshot
	if s.hooks.SpotStatus != nil {
		st = s.hooks.SpotStatus()
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleImageRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
		return
	}
	params := parseFlexParams(r)
	wid := strings.TrimSpace(paramStr(r, params, "widget_id"))
	if s.hooks.RefreshImage != nil {
		s.hooks.RefreshImage(wid)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "widget_id": wid})
}

func (s *Server) applyConfig(newCfg *config.Config) {
	s.mu.Lock()
	s.cfg = newCfg
	s.mu.Unlock()
	if s.hooks.ReloadConfig != nil {
		s.hooks.ReloadConfig(newCfg)
	}
}

func parseFlexParams(r *http.Request) map[string]any {
	out := make(map[string]any)
	if r.Body == nil || r.Method == http.MethodGet {
		return out
	}
	ct := r.Header.Get("Content-Type")
	if strings.Contains(ct, "application/x-www-form-urlencoded") {
		_ = r.ParseForm()
		for k, vals := range r.PostForm {
			if len(vals) > 0 {
				out[k] = vals[0]
			}
		}
		return out
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

func paramStr(r *http.Request, body map[string]any, key string) string {
	if q := r.URL.Query().Get(key); q != "" {
		return q
	}
	if v, ok := body[key]; ok && v != nil {
		switch t := v.(type) {
		case string:
			return t
		case float64:
			if t == float64(int64(t)) {
				return strconv.FormatInt(int64(t), 10)
			}
			return strconv.FormatFloat(t, 'f', -1, 64)
		case bool:
			if t {
				return "true"
			}
			return "false"
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, code int, val any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(val)
}
