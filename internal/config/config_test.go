package config

import (
	"testing"
)

func TestDefaultConfigValidates(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.NormalizeAndValidate(); err != nil {
		t.Fatalf("DefaultConfig failed validation: %v", err)
	}
	topics := cfg.AllSubscribeTopics()
	if len(topics) == 0 {
		t.Fatalf("expected subscribe topics from DefaultConfig")
	}
}

func TestParseCustomYAMLOverridesPages(t *testing.T) {
	raw := []byte(`
mqtt:
  broker: tcp://10.0.0.5:1883
  base_topic: test/kindle
pages:
  - title: Test
    widgets:
      - type: switch
        label: Relay
        state_topic: stat/relay/POWER
        command_topic: cmnd/relay/POWER
`)
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(cfg.Pages) != 1 || len(cfg.Pages[0].Widgets) != 1 {
		t.Fatalf("unexpected pages/widgets count: %+v", cfg.Pages)
	}
	w := cfg.Pages[0].Widgets[0]
	if w.PayloadOn != "ON" || w.ValueOn != "ON" || w.Span != 1 {
		t.Fatalf("expected normalized switch defaults, got %+v", w)
	}
}

func TestParseRejectsUnsupportedWidgetType(t *testing.T) {
	raw := []byte(`
mqtt:
  broker: tcp://10.0.0.5:1883
pages:
  - title: Bad
    widgets:
      - type: video_stream
        label: Invalid
`)
	if _, err := Parse(raw); err == nil {
		t.Fatalf("expected error for unsupported widget type")
	}
}

func TestParseGaugeChartAndFloorplan(t *testing.T) {
	raw := []byte(`
mqtt:
  broker: tcp://10.0.0.5:1883
pages:
  - title: Grafy
    widgets:
      - type: gauge
        label: Výkon
        state_topic: home/power
      - type: chart
        label: Teplota
        state_topic: home/temp
  - title: Půdorys
    type: floorplan
    floorplan:
      shapes:
        - kind: room
          label: Obývák
          x: 50
          y: 50
          w: 400
          h: 300
      pins:
        - x: 200
          y: 180
          widget:
            type: sensor
            label: Teplota
            state_topic: home/living/temp
`)
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if cfg.Pages[0].Widgets[0].Max != 100 {
		t.Fatalf("expected default gauge max=100, got %v", cfg.Pages[0].Widgets[0].Max)
	}
	topics := cfg.AllSubscribeTopics()
	if len(topics) != 3 {
		t.Fatalf("expected 3 topics including floorplan pin, got %v", topics)
	}
}

func TestMultiBrokerConfigAndRouting(t *testing.T) {
	raw := []byte(`
mqtt:
  id: cloud
  name: HiveMQ Cloud
  broker: ssl://example.hivemq.cloud:8883
  brokers:
    - id: local
      name: Local Mosquitto
      broker: tcp://192.168.1.10:1883
pages:
  - title: Multi
    widgets:
      - type: sensor
        label: Cloud Temp
        state_topic: cloud/temp
      - type: switch
        broker_id: local
        label: Local Light
        state_topic: zigbee/light
        command_topic: zigbee/light/set
`)
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse multi-broker failed: %v", err)
	}
	brokers := cfg.EffectiveBrokers()
	if len(brokers) != 2 || brokers[0].ID != "cloud" || brokers[1].ID != "local" {
		t.Fatalf("unexpected effective brokers: %+v", brokers)
	}
	byBroker := cfg.SubscribeTopicsByBroker()
	if len(byBroker["cloud"]) != 1 || byBroker["cloud"][0] != "cloud/temp" {
		t.Fatalf("unexpected cloud topics: %+v", byBroker)
	}
	if len(byBroker["local"]) != 1 || byBroker["local"][0] != "zigbee/light" {
		t.Fatalf("unexpected local topics: %+v", byBroker)
	}

	// Unknown broker_id must fail validation.
	cfg.Pages[0].Widgets[1].BrokerID = "missing"
	if err := cfg.NormalizeAndValidate(); err == nil {
		t.Fatalf("expected error for unknown broker_id")
	}
}

func TestTimerCalendarSchedulesAndLandscapeConfig(t *testing.T) {
	raw := []byte(`
mqtt:
  broker: tcp://10.0.0.5:1883
calendar:
  enabled: true
  urls:
    - webcal://example.com/family.ics
schedules:
  - name: Nocni vypnuti
    enabled: true
    hour: 22
    minute: 30
    days: [1, 2, 3, 4, 5]
    widget_id: sw_lamp
    state: OFF
device:
  orientation: landscape
  theme_mode: auto_night
  night_start_hour: 22
  night_end_hour: 6
pages:
  - title: Terasa
    columns: 3
    widgets:
      - id: sw_lamp
        type: switch
        label: Lampicka
      - id: t_lamp
        type: timer
        label: Minutka lampa
        target_widget_id: sw_lamp
      - id: cal
        type: calendar
        label: Kalendar
        span: 3
`)
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	lw, lh := cfg.Device.LogicalSize()
	if lw != 800 || lh != 600 {
		t.Fatalf("expected 800x600 for landscape, got %dx%d", lw, lh)
	}
	if !cfg.Device.IsNightHour(23) || !cfg.Device.IsNightHour(5) || cfg.Device.IsNightHour(12) {
		t.Fatalf("unexpected IsNightHour behavior for 22..6 range")
	}
	if cfg.Pages[0].Columns != 3 {
		t.Fatalf("expected Columns=3 preserved, got %d", cfg.Pages[0].Columns)
	}
	timerW := cfg.Pages[0].Widgets[1]
	if len(timerW.TimerPresets) != 3 || timerW.TargetState != "OFF" {
		t.Fatalf("expected timer defaults, got %+v", timerW)
	}
	if len(cfg.Schedules) != 1 || cfg.Schedules[0].ID != "sched_1" || cfg.Schedules[0].Hour != 22 || cfg.Schedules[0].Minute != 30 {
		t.Fatalf("unexpected normalized schedules: %+v", cfg.Schedules)
	}
	if !cfg.Calendar.Enabled || len(cfg.Calendar.URLs) != 1 {
		t.Fatalf("unexpected calendar config: %+v", cfg.Calendar)
	}
}

func TestExampleConfigValidates(t *testing.T) {
	cfg, err := Load("../../config.example.yaml")
	if err != nil {
		t.Fatalf("config.example.yaml must remain valid: %v", err)
	}
	if len(cfg.Pages) < 3 {
		t.Fatalf("expected >= 3 pages in config.example.yaml, got %d", len(cfg.Pages))
	}
}

func TestNormalizeLanguage(t *testing.T) {
	cases := map[string]string{
		"":      "en",
		"en":    "en",
		"en-US": "en",
		"fr":    "en",
		"cs":    "cs",
		"cz":    "cs",
		"cs-CZ": "cs",
		"Czech": "cs",
	}
	for in, want := range cases {
		if got := NormalizeLanguage(in); got != want {
			t.Fatalf("NormalizeLanguage(%q) = %q, want %q", in, got, want)
		}
	}
	cfg := DefaultConfig()
	cfg.Device.Language = "cz"
	if err := cfg.NormalizeAndValidate(); err != nil {
		t.Fatalf("NormalizeAndValidate failed: %v", err)
	}
	if cfg.Device.Language != "cs" {
		t.Fatalf("expected normalized language cs, got %q", cfg.Device.Language)
	}
}

func TestWidgetPrecisionSerializationAndHelpers(t *testing.T) {
	raw := []byte(`
mqtt:
  broker: tcp://10.0.0.5:1883
pages:
  - title: Precision
    widgets:
      - id: auto_s
        type: sensor
        label: Auto
        state_topic: t/1
      - id: zero_s
        type: sensor
        label: Zero
        state_topic: t/2
        precision: 0
        secondary_topic: t/2
        secondary_precision: 0
      - id: two_s
        type: sensor
        label: Two
        state_topic: t/3
        precision: 2
        secondary_topic: t/3
        secondary_precision: 9
`)
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	ws := cfg.Pages[0].Widgets
	if ws[0].EffectivePrecision() != -1 || ws[0].EffectiveSecondaryPrecision() != -1 {
		t.Fatalf("expected omitted precision to be -1 (auto), got %d / %d", ws[0].EffectivePrecision(), ws[0].EffectiveSecondaryPrecision())
	}
	if ws[1].EffectivePrecision() != 0 || ws[1].EffectiveSecondaryPrecision() != 0 {
		t.Fatalf("expected explicit precision 0 to stay 0, got %d / %d", ws[1].EffectivePrecision(), ws[1].EffectiveSecondaryPrecision())
	}
	if ws[2].EffectivePrecision() != 2 || ws[2].EffectiveSecondaryPrecision() != 6 {
		t.Fatalf("expected precision 2 and clamped 6, got %d / %d", ws[2].EffectivePrecision(), ws[2].EffectiveSecondaryPrecision())
	}
}
