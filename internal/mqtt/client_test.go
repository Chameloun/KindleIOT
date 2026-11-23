package mqtt

import (
	"testing"

	"kindleiot/internal/config"
)

func TestExtractValue(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		path    string
		want    string
	}{
		{"plain", " 22.4 ", "", "22.4"},
		{"top_field", `{"temperature": 21.5, "humidity": 48}`, "$.temperature", "21.5"},
		{"int_field", `{"humidity": 48}`, "humidity", "48"},
		{"nested_array", `{"readings":[{"val":12.3},{"val":99}]}`, "$.readings[1].val", "99"},
		{"bool_field", `{"contact": false}`, "$.contact", "false"},
		{"missing_field", `{"a":1}`, "$.b", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractValue(tc.payload, tc.path)
			if got != tc.want {
				t.Fatalf("ExtractValue(%q, %q) = %q, want %q", tc.payload, tc.path, got, tc.want)
			}
		})
	}
}

func TestFormatCommand(t *testing.T) {
	if got := FormatCommand("{{value}}", 21.5, 1); got != "21.5" {
		t.Fatalf("got %q, want 21.5", got)
	}
	if got := FormatCommand(`{"target": {{value}}}`, 22, 0); got != `{"target": 22}` {
		t.Fatalf("got %q", got)
	}
}

func TestNormalizeBrokerURL(t *testing.T) {
	cases := map[string]string{
		"tcp://192.168.1.10:1883":             "tcp://192.168.1.10:1883",
		"mqtt://broker.hivemq.com":            "tcp://broker.hivemq.com:1883",
		"mqtts://abc.s1.eu.hivemq.cloud":      "ssl://abc.s1.eu.hivemq.cloud:8883",
		"tls://abc.s1.eu.hivemq.cloud:8883":   "ssl://abc.s1.eu.hivemq.cloud:8883",
		"abc.s1.eu.hivemq.cloud":              "ssl://abc.s1.eu.hivemq.cloud:8883",
		"abc.s1.eu.hivemq.cloud:8883":         "ssl://abc.s1.eu.hivemq.cloud:8883",
		"192.168.1.10:1883":                   "tcp://192.168.1.10:1883",
	}
	for in, want := range cases {
		if got := NormalizeBrokerURL(in); got != want {
			t.Fatalf("NormalizeBrokerURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEmbeddedRootCAsLoad(t *testing.T) {
	pool := buildRootCAs()
	if pool == nil {
		t.Fatalf("expected non-nil root CA pool")
	}
}

func TestGetHistoryExtractsSeries(t *testing.T) {
	c := NewClient(config.DefaultConfig().MQTT, nil, nil, CommandHandler{})
	c.SetLocalValue("home/power", `{"w": 120}`)
	c.SetLocalValue("home/power", `{"w": 245.5}`)
	c.SetLocalValue("home/power", `{"w": "bad"}`)
	c.SetLocalValue("home/power", `{"w": 190}`)

	pts := c.GetHistory("home/power", "$.w", 10)
	if len(pts) != 3 {
		t.Fatalf("expected 3 numeric points, got %d", len(pts))
	}
	if pts[0].Value != 120 || pts[1].Value != 245.5 || pts[2].Value != 190 {
		t.Fatalf("unexpected series values: %+v", pts)
	}
}

func TestManagerMultiBrokerRouting(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.MQTT.ID = "cloud"
	cfg.MQTT.Brokers = []config.MQTTConfig{
		{ID: "local", Name: "Local", Broker: "tcp://127.0.0.1:1883"},
	}
	if err := cfg.NormalizeAndValidate(); err != nil {
		t.Fatalf("NormalizeAndValidate failed: %v", err)
	}

	mgr := NewManager(cfg, nil, CommandHandler{})
	mgr.SetLocalValue("", "shared/temp", "21.0")
	mgr.SetLocalValue("local", "shared/temp", "19.5")

	vCloud, ok1 := mgr.GetTopic("cloud", "shared/temp")
	vLocal, ok2 := mgr.GetTopic("local", "shared/temp")
	if !ok1 || !ok2 || vCloud.Payload != "21.0" || vLocal.Payload != "19.5" {
		t.Fatalf("expected isolated per-broker caches, got cloud=%+v local=%+v", vCloud, vLocal)
	}

	snap := mgr.Snapshot()
	if snap.TotalCount != 2 || len(snap.Brokers) != 2 {
		t.Fatalf("expected 2 brokers in snapshot, got %+v", snap)
	}
}
