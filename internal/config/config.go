package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config defines the complete KindleIOT application configuration.
type Config struct {
	MQTT      MQTTConfig       `yaml:"mqtt" json:"mqtt"`
	HomeKit   HomeKitConfig    `yaml:"homekit" json:"homekit"`
	Weather   WeatherConfig    `yaml:"weather" json:"weather"`
	Calendar  CalendarConfig   `yaml:"calendar" json:"calendar"`
	Schedules []ScheduleConfig `yaml:"schedules,omitempty" json:"schedules,omitempty"`
	Device    DeviceConfig     `yaml:"device" json:"device"`
	Web       WebConfig        `yaml:"web" json:"web"`
	Pages     []PageConfig     `yaml:"pages" json:"pages"`
}

// CalendarConfig configures iCal (.ics) calendar feed polling.
type CalendarConfig struct {
	Enabled bool     `yaml:"enabled" json:"enabled"`
	URLs    []string `yaml:"urls,omitempty" json:"urls,omitempty"`
}

// ScheduleConfig defines a daily recurring automation schedule evaluated on the Kindle.
type ScheduleConfig struct {
	ID       string `yaml:"id" json:"id"`
	Name     string `yaml:"name" json:"name"`
	Enabled  bool   `yaml:"enabled" json:"enabled"`
	Hour     int    `yaml:"hour" json:"hour"`                   // 0..23
	Minute   int    `yaml:"minute" json:"minute"`               // 0..59
	Days     []int  `yaml:"days,omitempty" json:"days,omitempty"` // 1=Mon..7=Sun (empty = every day)
	WidgetID string `yaml:"widget_id,omitempty" json:"widget_id,omitempty"`
	State    string `yaml:"state,omitempty" json:"state,omitempty"` // ON, OFF, TOGGLE
	BrokerID string `yaml:"broker_id,omitempty" json:"broker_id,omitempty"`
	Topic    string `yaml:"topic,omitempty" json:"topic,omitempty"`
	Payload  string `yaml:"payload,omitempty" json:"payload,omitempty"`
}

// WeatherConfig configures the built-in Open-Meteo weather forecast (no API key needed).
type WeatherConfig struct {
	Enabled      bool    `yaml:"enabled" json:"enabled"`
	LocationName string  `yaml:"location_name,omitempty" json:"location_name,omitempty"`
	Latitude     float64 `yaml:"latitude" json:"latitude"`
	Longitude    float64 `yaml:"longitude" json:"longitude"`
}

// HomeKitConfig configures the built-in Apple HomeKit Bridge (HAP server).
type HomeKitConfig struct {
	Enabled  bool   `yaml:"enabled" json:"enabled"`
	Pin      string `yaml:"pin" json:"pin"`
	Port     int    `yaml:"port" json:"port"`
	StoreDir string `yaml:"store_dir,omitempty" json:"store_dir,omitempty"`
}

// MQTTConfig configures connection to a universal MQTT broker (and optional extra broker instances).
type MQTTConfig struct {
	ID                     string       `yaml:"id,omitempty" json:"id,omitempty"`
	Name                   string       `yaml:"name,omitempty" json:"name,omitempty"`
	Broker                 string       `yaml:"broker" json:"broker"`
	ClientID               string       `yaml:"client_id" json:"client_id"`
	Username               string       `yaml:"username" json:"username"`
	Password               string       `yaml:"password" json:"password"`
	InsecureTLS            bool         `yaml:"insecure_tls" json:"insecure_tls"`
	KeepAliveSec           int          `yaml:"keep_alive_sec" json:"keep_alive_sec"`
	BaseTopic              string       `yaml:"base_topic" json:"base_topic"`
	HomeAssistantDiscovery bool         `yaml:"homeassistant_discovery" json:"homeassistant_discovery"`
	DiscoveryPrefix        string       `yaml:"discovery_prefix" json:"discovery_prefix"`
	Brokers                []MQTTConfig `yaml:"brokers,omitempty" json:"brokers,omitempty"`
}

// SameConnection reports whether two single-broker configurations share identical connection parameters.
func (m MQTTConfig) SameConnection(other MQTTConfig) bool {
	return m.ID == other.ID &&
		m.Broker == other.Broker &&
		m.ClientID == other.ClientID &&
		m.Username == other.Username &&
		m.Password == other.Password &&
		m.InsecureTLS == other.InsecureTLS &&
		m.KeepAliveSec == other.KeepAliveSec &&
		m.BaseTopic == other.BaseTopic &&
		m.HomeAssistantDiscovery == other.HomeAssistantDiscovery &&
		m.DiscoveryPrefix == other.DiscoveryPrefix
}

// DeviceConfig controls Kindle hardware behavior and e-ink rendering parameters.
type DeviceConfig struct {
	Title                string `yaml:"title" json:"title"`
	Width                int    `yaml:"width" json:"width"`
	Height               int    `yaml:"height" json:"height"`
	PreventSleep         bool   `yaml:"prevent_sleep" json:"prevent_sleep"`
	InitialBrightness    int    `yaml:"initial_brightness" json:"initial_brightness"`
	BacklightTimeoutSec  int    `yaml:"backlight_timeout_sec,omitempty" json:"backlight_timeout_sec,omitempty"`
	StandbyTimeoutSec    int    `yaml:"standby_timeout_sec,omitempty" json:"standby_timeout_sec,omitempty"`
	DayBrightness        int    `yaml:"day_brightness,omitempty" json:"day_brightness,omitempty"`
	NightModeEnabled     bool   `yaml:"night_mode_enabled,omitempty" json:"night_mode_enabled,omitempty"`
	NightBrightness      int    `yaml:"night_brightness,omitempty" json:"night_brightness,omitempty"`
	NightStartHour       int    `yaml:"night_start_hour,omitempty" json:"night_start_hour,omitempty"`
	NightEndHour         int    `yaml:"night_end_hour,omitempty" json:"night_end_hour,omitempty"`
	PinCode              string `yaml:"pin_code,omitempty" json:"pin_code,omitempty"`
	PinLockStandby       bool   `yaml:"pin_lock_standby,omitempty" json:"pin_lock_standby,omitempty"`
	Orientation          string `yaml:"orientation,omitempty" json:"orientation,omitempty"` // portrait (default), landscape, landscape_cw
	ThemeMode            string `yaml:"theme_mode,omitempty" json:"theme_mode,omitempty"`   // light (default), dark, auto_night
	TelemetryIntervalSec int    `yaml:"telemetry_interval_sec" json:"telemetry_interval_sec"`
	FullRefreshEvery     int    `yaml:"full_refresh_every" json:"full_refresh_every"`
}

// IsNightHour reports whether the given clock hour (0..23) falls inside the configured night window.
func (d DeviceConfig) IsNightHour(hour int) bool {
	if d.NightStartHour > d.NightEndHour {
		return hour >= d.NightStartHour || hour < d.NightEndHour
	}
	if d.NightStartHour < d.NightEndHour {
		return hour >= d.NightStartHour && hour < d.NightEndHour
	}
	return false
}

// LogicalSize returns the UI canvas dimensions taking Orientation (portrait vs landscape) into account.
func (d DeviceConfig) LogicalSize() (int, int) {
	w, h := d.Width, d.Height
	if w <= 0 {
		w = 600
	}
	if h <= 0 {
		h = 800
	}
	shortSide, longSide := w, h
	if w > h {
		shortSide, longSide = h, w
	}
	if d.Orientation == "landscape" || d.Orientation == "landscape_cw" {
		return longSide, shortSide
	}
	return shortSide, longSide
}

// ActiveBrightnessForHour returns the target awake frontlight level (0..24) for the given clock hour (0..23).
func (d DeviceConfig) ActiveBrightnessForHour(hour int) int {
	if d.NightModeEnabled && d.IsNightHour(hour) {
		return clampInt(d.NightBrightness, 0, 24)
	}
	if d.DayBrightness > 0 {
		return clampInt(d.DayBrightness, 0, 24)
	}
	if d.InitialBrightness >= 0 {
		return clampInt(d.InitialBrightness, 0, 24)
	}
	return 14
}

// WebConfig configures the built-in browser simulator and YAML config editor.
type WebConfig struct {
	Listen string `yaml:"listen" json:"listen"`
}

// PageConfig represents one tab/screen of IoT widgets or a building floorplan.
type PageConfig struct {
	ID        string           `yaml:"id" json:"id"`
	Title     string           `yaml:"title" json:"title"`
	Type      string           `yaml:"type,omitempty" json:"type,omitempty"` // grid (default), floorplan
	Columns   int              `yaml:"columns" json:"columns"`
	Widgets   []WidgetConfig   `yaml:"widgets" json:"widgets"`
	Floorplan *FloorplanConfig `yaml:"floorplan,omitempty" json:"floorplan,omitempty"`
}

// FloorplanConfig holds architectural vector shapes and placed IoT pins in normalized 0..1000 space.
type FloorplanConfig struct {
	Shapes []FloorShape `yaml:"shapes,omitempty" json:"shapes,omitempty"`
	Pins   []FloorPin   `yaml:"pins,omitempty" json:"pins,omitempty"`
}

// FloorShape represents a room box, wall line, door opening, or label on the floorplan.
type FloorShape struct {
	ID    string `yaml:"id" json:"id"`
	Kind  string `yaml:"kind" json:"kind"` // room, wall, door, text
	Label string `yaml:"label,omitempty" json:"label,omitempty"`
	X     int    `yaml:"x,omitempty" json:"x,omitempty"`
	Y     int    `yaml:"y,omitempty" json:"y,omitempty"`
	W     int    `yaml:"w,omitempty" json:"w,omitempty"`
	H     int    `yaml:"h,omitempty" json:"h,omitempty"`
	X2    int    `yaml:"x2,omitempty" json:"x2,omitempty"`
	Y2    int    `yaml:"y2,omitempty" json:"y2,omitempty"`
}

// FloorPin places an interactive widget or sensor badge at (X, Y) in 0..1000 floorplan coordinates.
type FloorPin struct {
	ID     string       `yaml:"id" json:"id"`
	X      int          `yaml:"x" json:"x"`
	Y      int          `yaml:"y" json:"y"`
	Widget WidgetConfig `yaml:"widget" json:"widget"`
}

// SelectOption represents a preset choice for a select widget.
type SelectOption struct {
	Label   string `yaml:"label" json:"label"`
	Payload string `yaml:"payload" json:"payload"`
}

// MacroAction represents one step inside a multi-action scene/macro button.
type MacroAction struct {
	WidgetID string `yaml:"widget_id,omitempty" json:"widget_id,omitempty"`
	State    string `yaml:"state,omitempty" json:"state,omitempty"` // ON, OFF, TOGGLE
	BrokerID string `yaml:"broker_id,omitempty" json:"broker_id,omitempty"`
	Topic    string `yaml:"topic,omitempty" json:"topic,omitempty"`
	Payload  string `yaml:"payload,omitempty" json:"payload,omitempty"`
}

// WidgetConfig defines a single universal MQTT widget on a page.
type WidgetConfig struct {
	ID                string            `yaml:"id" json:"id"`
	Type              string            `yaml:"type" json:"type"` // switch, button, stepper, sensor, select, gauge, chart, weather, timer, calendar
	BrokerID          string            `yaml:"broker_id,omitempty" json:"broker_id,omitempty"`
	Icon              string            `yaml:"icon,omitempty" json:"icon,omitempty"`
	Label             string            `yaml:"label" json:"label"`
	Subtitle          string            `yaml:"subtitle,omitempty" json:"subtitle,omitempty"`
	Span              int               `yaml:"span,omitempty" json:"span,omitempty"`
	StateTopic        string            `yaml:"state_topic,omitempty" json:"state_topic,omitempty"`
	StateJSONPath     string            `yaml:"state_json_path,omitempty" json:"state_json_path,omitempty"`
	CommandTopic      string            `yaml:"command_topic,omitempty" json:"command_topic,omitempty"`
	Payload           string            `yaml:"payload,omitempty" json:"payload,omitempty"`
	PayloadOn         string            `yaml:"payload_on,omitempty" json:"payload_on,omitempty"`
	PayloadOff        string            `yaml:"payload_off,omitempty" json:"payload_off,omitempty"`
	ValueOn           string            `yaml:"value_on,omitempty" json:"value_on,omitempty"`
	ValueOff          string            `yaml:"value_off,omitempty" json:"value_off,omitempty"`
	Confirm           bool              `yaml:"confirm,omitempty" json:"confirm,omitempty"`
	Min               float64           `yaml:"min,omitempty" json:"min,omitempty"`
	Max               float64           `yaml:"max,omitempty" json:"max,omitempty"`
	Step              float64           `yaml:"step,omitempty" json:"step,omitempty"`
	Precision         int               `yaml:"precision,omitempty" json:"precision,omitempty"`
	Unit              string            `yaml:"unit,omitempty" json:"unit,omitempty"`
	Format            string            `yaml:"format,omitempty" json:"format,omitempty"`
	CommandTemplate   string            `yaml:"command_template,omitempty" json:"command_template,omitempty"`
	SecondaryTopic    string            `yaml:"secondary_topic,omitempty" json:"secondary_topic,omitempty"`
	SecondaryJSONPath string            `yaml:"secondary_json_path,omitempty" json:"secondary_json_path,omitempty"`
	SecondaryUnit     string            `yaml:"secondary_unit,omitempty" json:"secondary_unit,omitempty"`
	ValueMap          map[string]string `yaml:"value_map,omitempty" json:"value_map,omitempty"`
	StaleSeconds      int               `yaml:"stale_seconds,omitempty" json:"stale_seconds,omitempty"`
	Options           []SelectOption    `yaml:"options,omitempty" json:"options,omitempty"`
	QoS               byte              `yaml:"qos,omitempty" json:"qos,omitempty"`
	Retain            bool              `yaml:"retain,omitempty" json:"retain,omitempty"`
	Optimistic        bool              `yaml:"optimistic,omitempty" json:"optimistic,omitempty"`
	HomeKit           bool              `yaml:"homekit,omitempty" json:"homekit,omitempty"`
	HomeKitType       string            `yaml:"homekit_type,omitempty" json:"homekit_type,omitempty"` // lightbulb (default), switch, outlet
	ShowInStandby     bool              `yaml:"show_in_standby,omitempty" json:"show_in_standby,omitempty"`
	AlertEnabled      bool              `yaml:"alert_enabled,omitempty" json:"alert_enabled,omitempty"`
	AlertMin          *float64          `yaml:"alert_min,omitempty" json:"alert_min,omitempty"`
	AlertMax          *float64          `yaml:"alert_max,omitempty" json:"alert_max,omitempty"`
	PinProtected      bool              `yaml:"pin_protected,omitempty" json:"pin_protected,omitempty"`
	MacroActions      []MacroAction     `yaml:"macro_actions,omitempty" json:"macro_actions,omitempty"`
	TargetWidgetID    string            `yaml:"target_widget_id,omitempty" json:"target_widget_id,omitempty"`
	TargetState       string            `yaml:"target_state,omitempty" json:"target_state,omitempty"`     // OFF (default), ON, TOGGLE
	TimerPresets      []int             `yaml:"timer_presets,omitempty" json:"timer_presets,omitempty"` // minutes, default [5,15,30]
}

// CheckAlert evaluates whether a numeric reading violates the widget's configured alert thresholds.
func (w WidgetConfig) CheckAlert(val float64) (bool, string) {
	if !w.AlertEnabled {
		return false, ""
	}
	if w.AlertMin != nil && val < *w.AlertMin {
		return true, fmt.Sprintf("< %g", *w.AlertMin)
	}
	if w.AlertMax != nil && val > *w.AlertMax {
		return true, fmt.Sprintf("> %g", *w.AlertMax)
	}
	return false, ""
}

// DefaultConfig returns a sensible starter configuration with example MQTT widgets.
func DefaultConfig() *Config {
	return &Config{
		MQTT: MQTTConfig{
			Broker:          "tcp://192.168.1.10:1883",
			ClientID:        "kindleiot-kt4",
			KeepAliveSec:    30,
			BaseTopic:       "kindleiot/panel",
			DiscoveryPrefix: "homeassistant",
		},
		HomeKit: HomeKitConfig{
			Enabled:  true,
			Pin:      "00102003",
			Port:     51827,
			StoreDir: "homekit-db",
		},
		Weather: WeatherConfig{
			Enabled:      true,
			LocationName: "Praha",
			Latitude:     50.0755,
			Longitude:    14.4378,
		},
		Device: DeviceConfig{
			Title:                "IoT Panel",
			Width:                600,
			Height:               800,
			PreventSleep:         true,
			InitialBrightness:    -1,
			BacklightTimeoutSec:  60,
			StandbyTimeoutSec:    300,
			DayBrightness:        14,
			NightModeEnabled:     true,
			NightBrightness:      3,
			NightStartHour:       22,
			NightEndHour:         6,
			TelemetryIntervalSec: 60,
			FullRefreshEvery:     20,
		},
		Web: WebConfig{
			Listen: ":8080",
		},
		Pages: []PageConfig{
			{
				ID:      "home",
				Title:   "Přehled",
				Columns: 2,
				Widgets: []WidgetConfig{
					{
						ID:                "living_temp",
						Type:              "sensor",
						Label:             "Obývák",
						StateTopic:        "home/livingroom/sensor",
						StateJSONPath:     "$.temperature",
						Unit:              "°C",
						SecondaryTopic:    "home/livingroom/sensor",
						SecondaryJSONPath: "$.humidity",
						SecondaryUnit:     "% RV",
					},
					{
						ID:            "outside_temp",
						Type:          "sensor",
						Label:         "Venku",
						StateTopic:    "home/outside/temperature",
						Unit:          "°C",
						StaleSeconds:  600,
					},
					{
						ID:            "main_light",
						Type:          "switch",
						Label:         "Hlavní světlo",
						Subtitle:      "Obývák",
						StateTopic:    "zigbee2mqtt/living_light",
						StateJSONPath: "$.state",
						CommandTopic:  "zigbee2mqtt/living_light/set",
						PayloadOn:     `{"state":"ON"}`,
						PayloadOff:    `{"state":"OFF"}`,
						ValueOn:       "ON",
						ValueOff:      "OFF",
					},
					{
						ID:           "lamp",
						Type:         "switch",
						Label:        "Stojací lampa",
						StateTopic:   "stat/lamp/POWER",
						CommandTopic: "cmnd/lamp/POWER",
						PayloadOn:    "ON",
						PayloadOff:   "OFF",
					},
					{
						ID:              "thermostat",
						Type:            "stepper",
						Label:           "Termostat",
						Span:            2,
						StateTopic:      "home/thermostat/target",
						CommandTopic:    "home/thermostat/target/set",
						CommandTemplate: "{{value}}",
						Min:             15,
						Max:             28,
						Step:            0.5,
						Precision:       1,
						Unit:            "°C",
					},
					{
						ID:           "scene_select",
						Type:         "select",
						Label:        "Režim",
						Span:         2,
						StateTopic:   "home/mode/state",
						CommandTopic: "home/mode/set",
						Options: []SelectOption{
							{Label: "Doma", Payload: "home"},
							{Label: "Večer", Payload: "evening"},
							{Label: "Noc", Payload: "night"},
							{Label: "Odchod", Payload: "away"},
						},
					},
				},
			},
			{
				ID:      "actions",
				Title:   "Akce",
				Columns: 2,
				Widgets: []WidgetConfig{
					{
						ID:           "all_off",
						Type:         "button",
						Label:        "Vypnout vše",
						Subtitle:     "Světla & zásuvky",
						CommandTopic: "home/scenes/set",
						Payload:      "all_off",
						Confirm:      true,
					},
					{
						ID:           "garage",
						Type:         "button",
						Label:        "Garážová vrata",
						Subtitle:     "Impulz",
						StateTopic:   "home/garage/door",
						CommandTopic: "home/garage/door/trigger",
						Payload:      "TRIGGER",
						Confirm:      true,
						ValueMap: map[string]string{
							"OPEN":   "Otevřeno",
							"CLOSED": "Zavřeno",
						},
					},
				},
			},
		},
	}
}

// Load reads and parses YAML configuration from path and applies defaults.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	return Parse(data)
}

// Parse unmarshals YAML bytes into a validated Config with defaults applied.
func Parse(data []byte) (*Config, error) {
	cfg := DefaultConfig()
	// Reset default pages if YAML provides pages explicitly.
	var probe struct {
		Pages []PageConfig `yaml:"pages"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	if len(probe.Pages) > 0 {
		cfg.Pages = nil
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	if err := cfg.NormalizeAndValidate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Save writes the configuration to disk as formatted YAML.
func Save(path string, cfg *Config) error {
	if err := cfg.NormalizeAndValidate(); err != nil {
		return err
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal yaml: %w", err)
	}
	return os.WriteFile(path, out, 0o644)
}

func normalizeBroker(b *MQTTConfig, defaultID, defaultName string) error {
	if strings.TrimSpace(b.Broker) == "" {
		return fmt.Errorf("mqtt broker %q must not be empty", defaultID)
	}
	if strings.TrimSpace(b.ID) == "" {
		b.ID = defaultID
	}
	if strings.TrimSpace(b.Name) == "" {
		b.Name = defaultName
	}
	if b.KeepAliveSec <= 0 {
		b.KeepAliveSec = 30
	}
	if strings.TrimSpace(b.BaseTopic) == "" {
		b.BaseTopic = "kindleiot/panel"
	}
	b.BaseTopic = strings.TrimRight(b.BaseTopic, "/")
	if strings.TrimSpace(b.DiscoveryPrefix) == "" {
		b.DiscoveryPrefix = "homeassistant"
	}
	return nil
}

// EffectiveBrokers returns all configured MQTT broker instances (primary first, followed by extras).
func (c *Config) EffectiveBrokers() []MQTTConfig {
	primary := c.MQTT
	primary.Brokers = nil
	if primary.ID == "" {
		primary.ID = "default"
	}
	if primary.Name == "" {
		primary.Name = "Hlavní broker"
	}
	out := make([]MQTTConfig, 0, 1+len(c.MQTT.Brokers))
	out = append(out, primary)
	for _, b := range c.MQTT.Brokers {
		b.Brokers = nil
		out = append(out, b)
	}
	return out
}

// PrimaryBrokerID returns the ID of the primary MQTT broker (default "default").
func (c *Config) PrimaryBrokerID() string {
	if strings.TrimSpace(c.MQTT.ID) != "" {
		return strings.TrimSpace(c.MQTT.ID)
	}
	return "default"
}

// NormalizeAndValidate fills missing defaults and validates structural constraints.
func (c *Config) NormalizeAndValidate() error {
	if err := normalizeBroker(&c.MQTT, "default", "Hlavní broker"); err != nil {
		return err
	}
	seenBrokers := map[string]struct{}{c.MQTT.ID: {}}
	for i := range c.MQTT.Brokers {
		b := &c.MQTT.Brokers[i]
		fallbackID := fmt.Sprintf("broker_%d", i+2)
		if err := normalizeBroker(b, fallbackID, fallbackID); err != nil {
			return fmt.Errorf("mqtt.brokers[%d]: %w", i, err)
		}
		if _, dup := seenBrokers[b.ID]; dup {
			return fmt.Errorf("duplicate mqtt broker id %q", b.ID)
		}
		seenBrokers[b.ID] = struct{}{}
	}

	if c.Device.Width <= 0 {
		c.Device.Width = 600
	}
	if c.HomeKit.Pin == "" {
		c.HomeKit.Pin = "00102003"
	}
	if len(c.HomeKit.Pin) != 8 {
		return fmt.Errorf("homekit.pin must be exactly 8 digits (got %q)", c.HomeKit.Pin)
	}
	for _, r := range c.HomeKit.Pin {
		if r < '0' || r > '9' {
			return fmt.Errorf("homekit.pin must contain only digits 0-9 (got %q)", c.HomeKit.Pin)
		}
	}
	if c.HomeKit.Port <= 0 {
		c.HomeKit.Port = 51827
	}
	if strings.TrimSpace(c.HomeKit.StoreDir) == "" {
		c.HomeKit.StoreDir = "homekit-db"
	}
	if strings.TrimSpace(c.Weather.LocationName) == "" {
		c.Weather.LocationName = "Praha"
	}
	if c.Weather.Latitude == 0 && c.Weather.Longitude == 0 {
		c.Weather.Latitude = 50.0755
		c.Weather.Longitude = 14.4378
	}
	c.Device.PinCode = strings.TrimSpace(c.Device.PinCode)
	if c.Device.Height <= 0 {
		c.Device.Height = 800
	}
	if c.Device.TelemetryIntervalSec <= 0 {
		c.Device.TelemetryIntervalSec = 60
	}
	if c.Device.FullRefreshEvery <= 0 {
		c.Device.FullRefreshEvery = 20
	}
	if c.Device.BacklightTimeoutSec < 0 {
		c.Device.BacklightTimeoutSec = 0
	}
	if c.Device.StandbyTimeoutSec < 0 {
		c.Device.StandbyTimeoutSec = 0
	}
	if c.Device.DayBrightness <= 0 {
		if c.Device.InitialBrightness > 0 {
			c.Device.DayBrightness = clampInt(c.Device.InitialBrightness, 1, 24)
		} else {
			c.Device.DayBrightness = 14
		}
	}
	c.Device.DayBrightness = clampInt(c.Device.DayBrightness, 0, 24)
	if c.Device.NightStartHour == 0 && c.Device.NightEndHour == 0 {
		c.Device.NightStartHour = 22
		c.Device.NightEndHour = 6
		if c.Device.NightBrightness == 0 {
			c.Device.NightBrightness = 3
		}
	}
	c.Device.NightBrightness = clampInt(c.Device.NightBrightness, 0, 24)
	c.Device.NightStartHour = clampInt(c.Device.NightStartHour, 0, 23)
	c.Device.NightEndHour = clampInt(c.Device.NightEndHour, 0, 23)
	switch c.Device.Orientation {
	case "", "portrait":
		c.Device.Orientation = "portrait"
	case "landscape", "landscape_cw":
	default:
		c.Device.Orientation = "portrait"
	}
	switch c.Device.ThemeMode {
	case "", "light":
		c.Device.ThemeMode = "light"
	case "dark", "auto_night":
	default:
		c.Device.ThemeMode = "light"
	}
	for si := range c.Schedules {
		sc := &c.Schedules[si]
		if strings.TrimSpace(sc.ID) == "" {
			sc.ID = fmt.Sprintf("sched_%d", si+1)
		}
		sc.Hour = clampInt(sc.Hour, 0, 23)
		sc.Minute = clampInt(sc.Minute, 0, 59)
		if strings.TrimSpace(sc.State) == "" {
			sc.State = "OFF"
		}
	}
	if strings.TrimSpace(c.Device.Title) == "" {
		c.Device.Title = "KindleIOT"
	}

	if len(c.Pages) == 0 {
		return fmt.Errorf("at least one page must be configured")
	}

	for pi := range c.Pages {
		p := &c.Pages[pi]
		if p.Columns <= 0 {
			p.Columns = 2
		}
		p.Columns = clampInt(p.Columns, 1, 3)
		if p.Type == "" {
			p.Type = "grid"
		}
		if p.Type != "grid" && p.Type != "floorplan" {
			return fmt.Errorf("page %q has unsupported type %q", p.ID, p.Type)
		}
		if p.ID == "" {
			p.ID = fmt.Sprintf("page_%d", pi+1)
		}
		if p.Title == "" {
			p.Title = p.ID
		}
		for wi := range p.Widgets {
			if err := normalizeWidget(&p.Widgets[wi], p.ID, wi, p.Columns, seenBrokers); err != nil {
				return err
			}
		}
		if p.Floorplan != nil {
			for si := range p.Floorplan.Shapes {
				s := &p.Floorplan.Shapes[si]
				if s.ID == "" {
					s.ID = fmt.Sprintf("%s_s%d", p.ID, si+1)
				}
				switch s.Kind {
				case "room", "wall", "door", "text":
				default:
					return fmt.Errorf("floorplan shape %q has unsupported kind %q", s.ID, s.Kind)
				}
			}
			for ki := range p.Floorplan.Pins {
				pin := &p.Floorplan.Pins[ki]
				if pin.ID == "" {
					pin.ID = fmt.Sprintf("%s_pin%d", p.ID, ki+1)
				}
				pin.X = clampInt(pin.X, 0, 1000)
				pin.Y = clampInt(pin.Y, 0, 1000)
				if err := normalizeWidget(&pin.Widget, p.ID+"_pin", ki, 2, seenBrokers); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func normalizeWidget(w *WidgetConfig, prefix string, idx int, maxCols int, knownBrokers map[string]struct{}) error {
	if w.ID == "" {
		w.ID = fmt.Sprintf("%s_w%d", prefix, idx+1)
	}
	if w.BrokerID != "" {
		if _, ok := knownBrokers[w.BrokerID]; !ok {
			return fmt.Errorf("widget %q references unknown broker_id %q", w.ID, w.BrokerID)
		}
	}
	if w.Span <= 0 {
		w.Span = 1
	}
	if w.Span > maxCols {
		w.Span = maxCols
	}
	if w.QoS > 2 {
		return fmt.Errorf("widget %q has invalid qos %d", w.ID, w.QoS)
	}
	switch w.Type {
	case "switch":
		if w.PayloadOn == "" {
			w.PayloadOn = "ON"
		}
		if w.PayloadOff == "" {
			w.PayloadOff = "OFF"
		}
		if w.ValueOn == "" {
			w.ValueOn = w.PayloadOn
		}
		if w.ValueOff == "" {
			w.ValueOff = w.PayloadOff
		}
		if strings.TrimSpace(w.StateTopic) == "" {
			w.StateTopic = "local/switch/" + w.ID
			w.Optimistic = true
		}
	case "button":
		if w.Payload == "" {
			w.Payload = "PRESS"
		}
	case "stepper":
		if w.Step == 0 {
			w.Step = 1
		}
		if w.Max == 0 && w.Min == 0 {
			w.Max = 100
		}
		if w.CommandTemplate == "" {
			w.CommandTemplate = "{{value}}"
		}
	case "gauge":
		if w.Max == 0 && w.Min == 0 {
			w.Max = 100
		}
	case "sensor", "chart", "weather", "calendar":
		// No extra defaults required.
	case "timer":
		if len(w.TimerPresets) == 0 {
			w.TimerPresets = []int{5, 15, 30}
		}
		if strings.TrimSpace(w.TargetState) == "" {
			w.TargetState = "OFF"
		}
	case "select":
		if len(w.Options) == 0 {
			return fmt.Errorf("select widget %q requires at least one option", w.ID)
		}
	default:
		return fmt.Errorf("widget %q has unsupported type %q", w.ID, w.Type)
	}
	return nil
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// SubscribeTopicsByBroker returns deduplicated subscription topics grouped by broker ID.
func (c *Config) SubscribeTopicsByBroker() map[string][]string {
	primaryID := c.PrimaryBrokerID()
	seen := make(map[string]map[string]struct{})
	out := make(map[string][]string)

	add := func(brokerID, t string) {
		t = strings.TrimSpace(t)
		if t == "" || strings.HasPrefix(t, "local/") {
			return
		}
		if strings.TrimSpace(brokerID) == "" {
			brokerID = primaryID
		}
		if seen[brokerID] == nil {
			seen[brokerID] = make(map[string]struct{})
		}
		if _, ok := seen[brokerID][t]; !ok {
			seen[brokerID][t] = struct{}{}
			out[brokerID] = append(out[brokerID], t)
		}
	}

	for _, p := range c.Pages {
		for _, w := range p.Widgets {
			add(w.BrokerID, w.StateTopic)
			add(w.BrokerID, w.SecondaryTopic)
		}
		if p.Floorplan != nil {
			for _, pin := range p.Floorplan.Pins {
				add(pin.Widget.BrokerID, pin.Widget.StateTopic)
				add(pin.Widget.BrokerID, pin.Widget.SecondaryTopic)
			}
		}
	}
	return out
}

// AllSubscribeTopics returns all deduplicated MQTT topics across all brokers.
func (c *Config) AllSubscribeTopics() []string {
	seen := make(map[string]struct{})
	var topics []string
	for _, list := range c.SubscribeTopicsByBroker() {
		for _, t := range list {
			if _, ok := seen[t]; !ok {
				seen[t] = struct{}{}
				topics = append(topics, t)
			}
		}
	}
	return topics
}
