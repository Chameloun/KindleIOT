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

func TestPageHiddenAndOrderPreserved(t *testing.T) {
	raw := []byte(`
mqtt:
  broker: tcp://10.0.0.5:1883
pages:
  - id: p1
    title: First Hidden
    hidden: true
    widgets:
      - type: sensor
        label: Temp
        state_topic: t/1
  - id: p2
    title: Second Visible
    widgets:
      - type: switch
        label: Light
        state_topic: local/switch/sw1
`)
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(cfg.Pages) != 2 || !cfg.Pages[0].Hidden || cfg.Pages[1].Hidden {
		t.Fatalf("expected page 0 hidden and page 1 visible, got %+v", cfg.Pages)
	}
	if cfg.Pages[0].ID != "p1" || cfg.Pages[1].ID != "p2" {
		t.Fatalf("page order not preserved: %+v", cfg.Pages)
	}
}

func TestBluetoothConfigDefaultsNormalizationAndValidation(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Bluetooth.Enabled || cfg.Bluetooth.RSSIThreshold != -65 || cfg.Bluetooth.AwayTimeoutSec != 30 {
		t.Fatalf("unexpected Bluetooth defaults: %+v", cfg.Bluetooth)
	}
	if !cfg.Bluetooth.WakeStandby || !cfg.Bluetooth.WakeBacklight || !cfg.Bluetooth.KeepAwake || !cfg.Bluetooth.PublishMQTT {
		t.Fatalf("expected proximity behavior defaults enabled: %+v", cfg.Bluetooth)
	}

	raw := []byte(`
mqtt:
  broker: tcp://10.0.0.5:1883
bluetooth:
  enabled: true
  rssi_threshold: -5
  away_timeout_sec: 1
  homekit_occupancy: true
  devices:
    - name: Watch
      irk: "irk:ec0234a3-57c8-ad05-3410-10a6-0a397d9b"
      mac: "aa-bb-cc-dd-ee-ff"
      enabled: true
pages:
  - title: Test
    widgets:
      - type: switch
        label: Lamp
`)
	parsed, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse valid bluetooth config failed: %v", err)
	}
	if parsed.Bluetooth.RSSIThreshold != -20 || parsed.Bluetooth.AwayTimeoutSec != 5 {
		t.Fatalf("expected clamped thresholds (-20, 5), got (%d, %d)", parsed.Bluetooth.RSSIThreshold, parsed.Bluetooth.AwayTimeoutSec)
	}
	if len(parsed.Bluetooth.Devices) != 1 || parsed.Bluetooth.Devices[0].ID != "ble_1" || parsed.Bluetooth.Devices[0].MAC != "AA:BB:CC:DD:EE:FF" {
		t.Fatalf("unexpected normalized BLE device: %+v", parsed.Bluetooth.Devices)
	}

	badIRK := []byte(`
mqtt:
  broker: tcp://10.0.0.5:1883
bluetooth:
  enabled: true
  devices:
    - name: Bad Watch
      irk: "not-valid-irk"
pages:
  - title: Test
    widgets:
      - type: switch
        label: Lamp
`)
	if _, err := Parse(badIRK); err == nil {
		t.Fatalf("expected invalid IRK error")
	}

	badMAC := []byte(`
mqtt:
  broker: tcp://10.0.0.5:1883
bluetooth:
  enabled: true
  devices:
    - name: Bad Tag
      mac: "ZZ:11:22"
pages:
  - title: Test
    widgets:
      - type: switch
        label: Lamp
`)
	if _, err := Parse(badMAC); err == nil {
		t.Fatalf("expected invalid MAC error")
	}
}

func TestGridRowsRowSpanAndExplicitPlacement(t *testing.T) {
	raw := []byte(`
mqtt:
  broker: tcp://10.0.0.5:1883
pages:
  - id: g3x3
    title: Grid 3x3
    columns: 5
    rows: 3
    widgets:
      - id: cal_big
        type: calendar
        label: Schedule
        span: 2
        row_span: 5
        grid_col: 4
        grid_row: -2
      - id: sw_tile
        type: switch
        label: Lamp
      - id: sen_wide
        type: sensor
        label: Outside
        state_topic: t/out
        span: 9
        row_span: 2
`)
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	pg := cfg.Pages[0]
	if pg.Columns != 5 || pg.Rows != 3 {
		t.Fatalf("expected columns=5 rows=3, got %d x %d", pg.Columns, pg.Rows)
	}
	w0 := pg.Widgets[0]
	// Span=2 on 5-col grid caps GridCol to max 3; RowSpan=5 caps to rows=3; GridRow=-2 clamps to 0.
	if w0.Span != 2 || w0.RowSpan != 3 || w0.GridCol == nil || *w0.GridCol != 3 || w0.GridRow == nil || *w0.GridRow != 0 {
		t.Fatalf("unexpected clamped w0: %+v", w0)
	}
	w2 := pg.Widgets[2]
	if w2.Span != 5 || w2.EffectiveRowSpan(pg.Rows) != 2 {
		t.Fatalf("unexpected w2 clamping: span=%d row_span=%d", w2.Span, w2.EffectiveRowSpan(pg.Rows))
	}

	// YAML round-trip via Save + Load preserves columns, rows, row_span, and explicit coordinates.
	p := t.TempDir() + "/cfg.yaml"
	if err := Save(p, cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	reloaded, err := Load(p)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	rw0 := reloaded.Pages[0].Widgets[0]
	if reloaded.Pages[0].Columns != 5 || reloaded.Pages[0].Rows != 3 || rw0.RowSpan != 3 || rw0.GridCol == nil || *rw0.GridCol != 3 {
		t.Fatalf("YAML round-trip mismatch: page=%+v w0=%+v", reloaded.Pages[0], rw0)
	}
}

func TestNewFeaturesConfigValidation(t *testing.T) {
	raw := []byte(`
mqtt:
  broker: tcp://10.0.0.5:1883
battery_guard:
  enabled: true
  min_percent: 25
  max_percent: 26
  topic: charger/set
spot_price:
  enabled: true
  currency: eur
  vat_percent: 21
  distribution_fee: 1.5
rules:
  - name: Watch Near
    enabled: true
    trigger: ble_near
    action_type: notify
    notify_text: Welcome
  - enabled: true
    trigger: topic_gt
    topic: home/power
    threshold: 3000
    action_type: page
    target_page: overview
pages:
  - id: p1
    title: Features
    columns: 2
    rows: 2
    widgets:
      - id: chk1
        type: checklist
        label: Shopping
        checklist_add_topic: home/list/add
        checklist_items:
          - text: Milk
            done: false
      - id: spot1
        type: spot_price
        label: Spot OTE
      - id: img1
        type: image
        label: Radar
        image_url: https://example.com/radar.png
        image_trigger_topic: cam/doorbell/snap
        image_fit: cover
      - id: http1
        type: sensor
        label: HTTP API
        http_url: http://192.168.1.50/status
      - id: hookbtn
        type: button
        label: Webhook
        webhook_url: http://192.168.1.50/toggle
`)
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if cfg.BatteryGuard.MaxPercent != 30 {
		t.Fatalf("expected max_percent clamped to min+5 (30), got %d", cfg.BatteryGuard.MaxPercent)
	}
	if cfg.SpotPrice.Currency != "EUR" || cfg.SpotPrice.EffectiveVAT() != 21 {
		t.Fatalf("unexpected spot config: %+v", cfg.SpotPrice)
	}
	if cfg.Rules[1].ID != "rule_2" {
		t.Fatalf("expected auto rule id rule_2, got %q", cfg.Rules[1].ID)
	}
	pg := cfg.Pages[0]
	if pg.Widgets[0].ChecklistItems[0].ID != "item_1" {
		t.Fatalf("expected auto checklist item ID, got %+v", pg.Widgets[0].ChecklistItems)
	}
	if pg.Widgets[2].ImageIntervalSec != 300 || !pg.Widgets[2].EffectiveImageDither() || pg.Widgets[2].ImageFit != "cover" {
		t.Fatalf("unexpected image widget defaults: %+v", pg.Widgets[2])
	}
	if pg.Widgets[3].StateTopic != "local/http/http1" || pg.Widgets[3].HTTPIntervalSec != 60 {
		t.Fatalf("unexpected HTTP poller defaults: %+v", pg.Widgets[3])
	}

	topics := cfg.AllSubscribeTopics()
	joined := make(map[string]bool)
	for _, tp := range topics {
		joined[tp] = true
	}
	for _, want := range []string{"home/list/add", "cam/doorbell/snap", "home/power"} {
		if !joined[want] {
			t.Fatalf("expected topic %q in subscriptions %v", want, topics)
		}
	}
}
