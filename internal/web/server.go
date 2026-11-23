package web

import (
	"bytes"
	"embed"
	"encoding/json"
	"image"
	"image/png"
	"io/fs"
	"net/http"
	"os"
	"sync"

	"kindleiot/internal/config"
	"kindleiot/internal/homekit"
	"kindleiot/internal/kindle"
	"kindleiot/internal/mqtt"
)

//go:embed static/*
var staticFS embed.FS

// Hooks connect web HTTP actions back to the running renderer and MQTT client.
type Hooks struct {
	GetImage      func() *image.Gray
	HandleTap     func(x, y int)
	ReloadConfig  func(newCfg *config.Config)
	HomeKitStatus func() homekit.Status
	ResetHomeKit  func() error
}

// Server serves the live screen mirror, visual drag-and-drop builder, MQTT inspector, and YAML editor.
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
	mux.HandleFunc("/api/homekit/reset", s.handleHomeKitReset)
	return mux
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
	resp := map[string]any{
		"mqtt_connected":   snap.AllConnected,
		"mqtt_any":         snap.AnyConnected,
		"mqtt_connected_n": snap.ConnectedCount,
		"mqtt_total_n":     snap.TotalCount,
		"mqtt_error":       snap.FirstError,
		"brokers":          snap.Brokers,
		"topics":           mergedTopics,
		"device":           hwStat,
		"homekit":          hkStat,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
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
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cfg)

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
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&newCfg)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) applyConfig(newCfg *config.Config) {
	s.mu.Lock()
	s.cfg = newCfg
	s.mu.Unlock()
	if s.hooks.ReloadConfig != nil {
		s.hooks.ReloadConfig(newCfg)
	}
}
