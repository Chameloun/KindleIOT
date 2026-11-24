package httppoll

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kindleiot/internal/config"
	"kindleiot/internal/mqtt"
)

func TestHTTPPollerAndWebhookTemplates(t *testing.T) {
	var (
		gotMethod string
		gotAuth   string
		gotCT     string
		gotBody   string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sensor":
			if r.Header.Get("X-Token") != "abc" {
				http.Error(w, "missing header", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"power": 412.5, "temp": 23.4}`))
		case "/hook":
			gotMethod = r.Method
			gotAuth = r.Header.Get("Authorization")
			gotCT = r.Header.Get("Content-Type")
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			w.WriteHeader(http.StatusOK)
		case "/fail":
			http.Error(w, "boom", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	cfg := &config.Config{
		MQTT: config.MQTTConfig{Broker: "tcp://127.0.0.1:1883"},
		Pages: []config.PageConfig{
			{
				ID: "p1",
				Widgets: []config.WidgetConfig{
					{
						ID:              "shelly_pwr",
						Type:            "sensor",
						Label:           "Shelly Power",
						HTTPURL:         srv.URL + "/sensor",
						HTTPIntervalSec: 30,
						HTTPHeaders:     map[string]string{"X-Token": "abc"},
						StateTopic:      "local/http/shelly_pwr",
						StateJSONPath:   "$.power",
					},
				},
			},
		},
	}
	mq := mqtt.NewManager(cfg, nil, mqtt.CommandHandler{})
	svc := NewService(cfg, mq)
	svc.SetHTTPClient(srv.Client())
	svc.PollNow()

	tv, ok := mq.GetTopic("", "local/http/shelly_pwr")
	if !ok {
		t.Fatalf("expected polled value in local/http/shelly_pwr")
	}
	if extracted := mqtt.ExtractValue(tv.Payload, "$.power"); extracted != "412.5" {
		t.Fatalf("expected extracted power 412.5, got %q (raw %q)", extracted, tv.Payload)
	}

	err := FireWebhook(context.Background(), srv.Client(), "", srv.URL+"/hook?mode={{state_lower}}", `{"turn":"{{state_lower}}"}`, map[string]string{
		"Authorization": "Bearer xyz",
	}, "ON")
	if err != nil {
		t.Fatalf("FireWebhook failed: %v", err)
	}
	if gotMethod != http.MethodPost || gotAuth != "Bearer xyz" || gotCT != "application/json" || gotBody != `{"turn":"on"}` {
		t.Fatalf("unexpected webhook call: method=%s auth=%s ct=%s body=%s", gotMethod, gotAuth, gotCT, gotBody)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := FireWebhook(ctx, srv.Client(), http.MethodGet, srv.URL+"/fail", "", nil, "PRESS"); err == nil {
		t.Fatalf("expected non-2xx error on /fail")
	}
}
