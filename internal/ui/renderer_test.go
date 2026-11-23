package ui

import (
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kindleiot/internal/calendar"
	"kindleiot/internal/config"
	"kindleiot/internal/kindle"
	"kindleiot/internal/mqtt"
	"kindleiot/internal/weather"
)

func maybeSavePreview(name string, img *image.Gray) {
	dir := os.Getenv("KINDLEIOT_PREVIEW_DIR")
	if dir == "" || img == nil {
		return
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		return
	}
	defer f.Close()
	_ = png.Encode(f, img)
}

func TestRendererRendersAndHitsTabs(t *testing.T) {
	cfg := config.DefaultConfig()
	mq := mqtt.NewManager(cfg, nil, mqtt.CommandHandler{})
	mq.SetLocalValue("", "home/livingroom/sensor", `{"temperature": 22.7, "humidity": 46}`)
	mq.SetLocalValue("", "zigbee2mqtt/living_light", `{"state": "ON"}`)

	hw := kindle.NewSystem()
	r, err := NewRenderer(cfg, mq, hw, Controller{})
	if err != nil {
		t.Fatalf("NewRenderer failed: %v", err)
	}

	img, _ := r.Render()
	maybeSavePreview("ui_main.png", img)
	if img.Bounds().Dx() != 600 || img.Bounds().Dy() != 800 {
		t.Fatalf("unexpected image dimensions: %v", img.Bounds())
	}

	// Verify second bottom tab switches activePage to 1.
	if !r.HandleTap(450, 765) {
		t.Fatalf("expected bottom tab tap to trigger redraw")
	}
	if r.activePage != 1 {
		t.Fatalf("expected activePage=1, got %d", r.activePage)
	}

	// Re-render page 2 (button with confirm=true).
	_, _ = r.Render()
	r.ShowNotice("Test notice", 5*time.Second)
	_, _ = r.Render()
}

func TestRendererConfirmButtonTwoStep(t *testing.T) {
	cfg := config.DefaultConfig()
	mq := mqtt.NewManager(cfg, nil, mqtt.CommandHandler{})
	hw := kindle.NewSystem()
	r, err := NewRenderer(cfg, mq, hw, Controller{})
	if err != nil {
		t.Fatalf("NewRenderer failed: %v", err)
	}

	r.mu.Lock()
	r.activePage = 1
	r.mu.Unlock()
	_, _ = r.Render()

	// Tap first button ("all_off", confirm=true) inside first card area.
	if !r.HandleTap(100, 120) {
		t.Fatalf("expected confirm prompt tap to return true")
	}
	if r.confirmID != "all_off" {
		t.Fatalf("expected confirmID=all_off, got %q", r.confirmID)
	}
}

func TestRendererStepperAndSelectAndSettings(t *testing.T) {
	cfg := config.DefaultConfig()
	mq := mqtt.NewManager(cfg, nil, mqtt.CommandHandler{})
	mq.SetLocalValue("", "home/thermostat/target", "21.5")
	mq.SetLocalValue("", "home/mode/state", "evening")
	mq.SetLocalValue("", "home/outside/temperature", "12.34")

	hw := kindle.NewSystem()
	var brightDelta int
	var refreshed bool
	r, err := NewRenderer(cfg, mq, hw, Controller{
		OnBrightness:  func(d int) { brightDelta += d },
		OnFullRefresh: func() { refreshed = true },
	})
	if err != nil {
		t.Fatalf("NewRenderer failed: %v", err)
	}

	img, _ := r.Render()
	if img == nil {
		t.Fatalf("expected non-nil rendered image")
	}

	// Open settings overlay (gear/sun icon near x=465, y=30) and adjust brightness.
	if !r.HandleTap(465, 30) || !r.showSettings {
		t.Fatalf("expected settings overlay to open")
	}
	_, _ = r.Render()
	// Tap '+' brightness button inside overlay (x ~ 505, y ~ 94)
	if !r.HandleTap(505, 94) || brightDelta != 3 {
		t.Fatalf("expected brightness delta +3, got %d", brightDelta)
	}
	// Tap refresh icon (x ~ 515, y ~ 30)
	if !r.HandleTap(515, 30) || !refreshed {
		t.Fatalf("expected full refresh callback to fire")
	}
}

func TestRendererGaugeChartAndFloorplan(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Pages = []config.PageConfig{
		{
			ID:      "viz",
			Title:   "Vizualizace",
			Columns: 2,
			Widgets: []config.WidgetConfig{
				{ID: "g1", Type: "gauge", Label: "Výkon", StateTopic: "home/power", Min: 0, Max: 3000, Unit: "W"},
				{ID: "c1", Type: "chart", Label: "Teplota", StateTopic: "home/temp", Unit: "°C", Precision: 1},
			},
		},
		{
			ID:    "fp",
			Title: "Půdorys",
			Type:  "floorplan",
			Floorplan: &config.FloorplanConfig{
				Shapes: []config.FloorShape{
					{ID: "r1", Kind: "room", Label: "Obývák", X: 50, Y: 50, W: 450, H: 400},
					{ID: "w1", Kind: "wall", X: 50, Y: 500, X2: 900, Y2: 500},
					{ID: "d1", Kind: "door", X: 400, Y: 500, X2: 500, Y2: 500},
					{ID: "t1", Kind: "text", Label: "Přízemí", X: 600, Y: 100},
				},
				Pins: []config.FloorPin{
					{
						ID: "p1", X: 250, Y: 250,
						Widget: config.WidgetConfig{
							ID: "fp_temp", Type: "sensor", Label: "Teplota", StateTopic: "home/temp", Unit: "°C",
						},
					},
					{
						ID: "p2", X: 700, Y: 300,
						Widget: config.WidgetConfig{
							ID: "fp_light", Type: "switch", Label: "Světlo",
							StateTopic: "home/light", CommandTopic: "home/light/set",
							PayloadOn: "ON", PayloadOff: "OFF", ValueOn: "ON", ValueOff: "OFF", Optimistic: true,
						},
					},
				},
			},
		},
	}
	if err := cfg.NormalizeAndValidate(); err != nil {
		t.Fatalf("NormalizeAndValidate failed: %v", err)
	}

	mq := mqtt.NewManager(cfg, nil, mqtt.CommandHandler{})
	mq.SetLocalValue("", "home/power", "1850")
	mq.SetLocalValue("", "home/temp", "20.2")
	mq.SetLocalValue("", "home/temp", "21.0")
	mq.SetLocalValue("", "home/temp", "21.6")
	mq.SetLocalValue("", "home/light", "ON")

	hw := kindle.NewSystem()
	r, err := NewRenderer(cfg, mq, hw, Controller{})
	if err != nil {
		t.Fatalf("NewRenderer failed: %v", err)
	}

	img1, _ := r.Render()
	maybeSavePreview("ui_charts.png", img1)
	if img1 == nil {
		t.Fatalf("expected rendered gauge+chart frame")
	}

	// Switch to floorplan tab.
	r.HandleTap(450, 765)
	img2, _ := r.Render()
	maybeSavePreview("ui_floorplan.png", img2)
	if img2 == nil {
		t.Fatalf("expected rendered floorplan frame")
	}
	if len(r.hits) < 3 {
		t.Fatalf("expected interactive hit targets on floorplan (tabs + switch pin)")
	}
}

func TestRendererStandbyAndAlerts(t *testing.T) {
	cfg := config.DefaultConfig()
	maxLim := 26.0
	cfg.Pages[0].Widgets[0].AlertEnabled = true
	cfg.Pages[0].Widgets[0].AlertMax = &maxLim
	cfg.Pages[0].Widgets[0].ShowInStandby = true

	mq := mqtt.NewManager(cfg, nil, mqtt.CommandHandler{})
	mq.SetLocalValue("", "home/livingroom/sensor", `{"temperature": 22.4, "humidity": 48}`)
	mq.SetLocalValue("", "home/outside/temperature", "8.5")

	hw := kindle.NewSystem()
	r, err := NewRenderer(cfg, mq, hw, Controller{})
	if err != nil {
		t.Fatalf("NewRenderer failed: %v", err)
	}

	// Enter standby screensaver and render
	if !r.SetStandby(true) || !r.IsStandby() {
		t.Fatalf("expected standby mode to activate")
	}
	imgStandby, _ := r.Render()
	maybeSavePreview("ui_standby.png", imgStandby)
	if imgStandby == nil {
		t.Fatalf("expected standby frame")
	}

	// Tap anywhere on standby screen to wake
	if !r.HandleTap(300, 400) || r.IsStandby() {
		t.Fatalf("expected tap on standby screen to exit standby")
	}

	// Re-enter standby and trigger threshold alert (> 26.0 °C)
	r.SetStandby(true)
	mq.SetLocalValue("", "home/livingroom/sensor", `{"temperature": 29.5, "humidity": 40}`)
	alerts := r.EvaluateAlerts()
	if len(alerts) != 1 {
		t.Fatalf("expected 1 newly triggered alert, got %v", alerts)
	}
	if r.IsStandby() {
		t.Fatalf("expected alert to automatically exit standby mode")
	}
	imgAlert, _ := r.Render()
	maybeSavePreview("ui_alert.png", imgAlert)
}

func TestRendererDetailWeatherMacroAndPin(t *testing.T) {
	if got := CzechNameDay(time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)); got != "Klement" {
		t.Fatalf("expected Czech name day for Nov 23 to be Klement, got %q", got)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"current": {"temperature_2m": 12.4, "weather_code": 2, "wind_speed_10m": 14.0},
			"daily": {
				"time": ["2026-11-23", "2026-11-24", "2026-11-25"],
				"weather_code": [2, 61, 0],
				"temperature_2m_max": [14.0, 9.5, 11.2],
				"temperature_2m_min": [5.0, 3.0, 1.5]
			}
		}`))
	}))
	defer ts.Close()

	cfg := config.DefaultConfig()
	cfg.Device.PinCode = "1234"
	cfg.Weather = config.WeatherConfig{
		Enabled:      true,
		LocationName: "Brno",
		Latitude:     49.1951,
		Longitude:    16.6068,
	}
	cfg.Pages = []config.PageConfig{
		{
			ID:      "ext",
			Title:   "Rozšíření",
			Columns: 2,
			Widgets: []config.WidgetConfig{
				{
					ID:            "living_temp",
					Type:          "sensor",
					Label:         "Obývák",
					StateTopic:    "home/livingroom/sensor",
					StateJSONPath: "$.temperature",
					Precision:     1,
					Unit:          "°C",
					ShowInStandby: true,
				},
				{
					ID:            "sw1",
					Type:          "switch",
					Label:         "Kotel",
					StateTopic:    "local/switch/sw1",
					PinProtected:  true,
					ShowInStandby: true,
				},
				{
					ID:            "sw2",
					Type:          "switch",
					Label:         "Noční lampa",
					StateTopic:    "local/switch/sw2",
					ShowInStandby: true,
				},
				{
					ID:    "macro_night",
					Type:  "button",
					Label: "Dobrou noc",
					MacroActions: []config.MacroAction{
						{WidgetID: "sw1", State: "OFF"},
						{WidgetID: "sw2", State: "ON"},
					},
				},
				{
					ID:    "wx",
					Type:  "weather",
					Label: "Počasí",
					Span:  2,
				},
			},
		},
	}
	if err := cfg.NormalizeAndValidate(); err != nil {
		t.Fatalf("NormalizeAndValidate: %v", err)
	}

	mq := mqtt.NewManager(cfg, nil, mqtt.CommandHandler{})
	mq.SetLocalValue("", "home/livingroom/sensor", `{"temperature": 20.0}`)
	mq.SetLocalValue("", "home/livingroom/sensor", `{"temperature": 21.5}`)
	mq.SetLocalValue("", "home/livingroom/sensor", `{"temperature": 23.0}`)
	mq.SetLocalValue("", "local/switch/sw1", "ON")
	mq.SetLocalValue("", "local/switch/sw2", "OFF")

	hw := kindle.NewSystem()
	widgetChanges := 0
	r, err := NewRenderer(cfg, mq, hw, Controller{
		OnWidgetChanged: func() { widgetChanges++ },
	})
	if err != nil {
		t.Fatalf("NewRenderer failed: %v", err)
	}

	svc := weather.NewService(cfg.Weather, nil)
	svc.SetEndpoint(ts.URL, ts.Client())
	svc.FetchNow()
	r.SetWeatherService(svc)

	// 1. Render main grid with rising sensor trend and Weather card
	if trend := r.sensorTrend(cfg.Pages[0].Widgets[0]); trend != "▲" {
		t.Fatalf("expected rising trend arrow ▲, got %q", trend)
	}
	imgWeather, _ := r.Render()
	maybeSavePreview("ui_weather.png", imgWeather)
	if imgWeather == nil {
		t.Fatalf("expected rendered weather page frame")
	}

	// 2. Render Standby screen with weather + Czech name day
	if !r.SetStandby(true) {
		t.Fatalf("expected SetStandby(true) to transition to standby")
	}
	imgStandbyWx, _ := r.Render()
	maybeSavePreview("ui_standby_weather.png", imgStandbyWx)
	if imgStandbyWx == nil {
		t.Fatalf("expected rendered standby weather frame")
	}
	if !r.HandleTap(300, 400) || r.IsStandby() {
		t.Fatalf("expected tap on standby screen to wake")
	}
	_, _ = r.Render()

	// 3. Tap sensor card (col 0, row 0) to open Sensor Detail modal
	if !r.HandleTap(150, 130) {
		t.Fatalf("expected tap on sensor card to trigger redraw")
	}
	r.mu.Lock()
	hasDetail := r.detailWidget != nil && r.detailWidget.ID == "living_temp"
	r.mu.Unlock()
	if !hasDetail {
		t.Fatalf("expected detailWidget to open for living_temp")
	}
	imgDetail, _ := r.Render()
	maybeSavePreview("ui_detail.png", imgDetail)

	// Close Sensor Detail modal via close button (471..571, 83..119)
	if !r.HandleTap(520, 100) {
		t.Fatalf("expected tap on detail close button to trigger redraw")
	}
	r.mu.Lock()
	hasDetailAfterClose := r.detailWidget != nil
	r.mu.Unlock()
	if hasDetailAfterClose {
		t.Fatalf("expected detailWidget to close")
	}
	_, _ = r.Render()

	// 4. Tap Macro Scene button ("Dobrou noc", row 1, col 1) -> sets sw1=OFF, sw2=ON
	if !r.HandleTap(440, 280) {
		t.Fatalf("expected macro button tap to return true")
	}
	if tv, ok := mq.GetTopic("", "local/switch/sw1"); !ok || tv.Payload != "OFF" {
		t.Fatalf("expected macro to turn sw1 OFF, got %+v", tv)
	}
	if tv, ok := mq.GetTopic("", "local/switch/sw2"); !ok || tv.Payload != "ON" {
		t.Fatalf("expected macro to turn sw2 ON, got %+v", tv)
	}
	if widgetChanges == 0 {
		t.Fatalf("expected OnWidgetChanged callback after macro execution")
	}
	_, _ = r.Render()

	// 5. Tap PIN-protected switch sw1 (row 0, col 1) -> opens PIN modal
	if !r.HandleTap(440, 130) {
		t.Fatalf("expected tap on PIN-protected switch to open PIN modal")
	}
	r.mu.Lock()
	pinOpen := r.pinModalOpen
	r.mu.Unlock()
	if !pinOpen {
		t.Fatalf("expected pinModalOpen=true")
	}
	imgPin, _ := r.Render()
	maybeSavePreview("ui_pin.png", imgPin)

	// Enter wrong PIN ("9999") by tapping key "9" (col 2, row 2 -> x=451, y=549) 4 times
	for i := 0; i < 4; i++ {
		if !r.HandleTap(451, 549) {
			t.Fatalf("expected PIN digit tap to return true")
		}
		_, _ = r.Render()
	}
	r.mu.Lock()
	pinErr := r.pinError
	pinStillOpen := r.pinModalOpen
	r.mu.Unlock()
	if !pinErr || !pinStillOpen {
		t.Fatalf("expected pinError=true and modal still open after wrong PIN, got err=%v open=%v", pinErr, pinStillOpen)
	}

	// Enter correct PIN "1234": 1=(145,277), 2=(298,277), 3=(451,277), 4=(145,413)
	for _, pt := range [][2]int{{145, 277}, {298, 277}, {451, 277}, {145, 413}} {
		if !r.HandleTap(pt[0], pt[1]) {
			t.Fatalf("expected PIN digit tap %v to return true", pt)
		}
		_, _ = r.Render()
	}
	r.mu.Lock()
	pinAfterOK := r.pinModalOpen
	r.mu.Unlock()
	if pinAfterOK {
		t.Fatalf("expected pinModalOpen=false after correct PIN")
	}
	if tv, ok := mq.GetTopic("", "local/switch/sw1"); !ok || tv.Payload != "ON" {
		t.Fatalf("expected pending sw1 toggle to execute after PIN unlock (OFF -> ON), got %+v", tv)
	}
}

func findHitCenter(r *Renderer, typ ActionType) (int, int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, h := range r.hits {
		if h.Type == typ {
			return (h.Rect.Min.X + h.Rect.Max.X) / 2, (h.Rect.Min.Y + h.Rect.Max.Y) / 2, true
		}
	}
	return 0, 0, false
}

func TestRendererTimersSchedulesCalendarLandscapeDarkMode(t *testing.T) {
	now := time.Now()
	baseHour := now.Truncate(time.Hour)

	var hourlyTimes, hourlyTemps, hourlyCodes, hourlyPrecip []string
	for i := 0; i < 24; i++ {
		ht := baseHour.Add(time.Duration(i) * time.Hour)
		hourlyTimes = append(hourlyTimes, fmt.Sprintf("%q", ht.Format("2006-01-02T15:04")))
		hourlyTemps = append(hourlyTemps, fmt.Sprintf("%.1f", 4.5+float64(i%12)*0.7))
		hourlyCodes = append(hourlyCodes, "2")
		hourlyPrecip = append(hourlyPrecip, fmt.Sprintf("%d", (i*10)%75))
	}
	var dailyDates, dailyCodes, dailyMax, dailyMin, dailyPrecip []string
	for d := 0; d < 7; d++ {
		dd := now.AddDate(0, 0, d)
		dailyDates = append(dailyDates, fmt.Sprintf("%q", dd.Format("2006-01-02")))
		dailyCodes = append(dailyCodes, fmt.Sprintf("%d", []int{0, 2, 61, 3, 1, 80, 0}[d]))
		dailyMax = append(dailyMax, fmt.Sprintf("%.1f", 9.0+float64(d%4)))
		dailyMin = append(dailyMin, fmt.Sprintf("%.1f", 1.5+float64(d%3)))
		dailyPrecip = append(dailyPrecip, fmt.Sprintf("%d", []int{10, 25, 80, 35, 15, 65, 5}[d]))
	}
	wxJSON := fmt.Sprintf(`{
		"current": {"temperature_2m": 7.4, "apparent_temperature": 4.9, "relative_humidity_2m": 74, "wind_speed_10m": 11.2, "weather_code": 2},
		"daily": {"time": [%s], "weather_code": [%s], "temperature_2m_max": [%s], "temperature_2m_min": [%s], "precipitation_probability_max": [%s]},
		"hourly": {"time": [%s], "temperature_2m": [%s], "weather_code": [%s], "precipitation_probability": [%s]}
	}`,
		strings.Join(dailyDates, ","), strings.Join(dailyCodes, ","), strings.Join(dailyMax, ","), strings.Join(dailyMin, ","), strings.Join(dailyPrecip, ","),
		strings.Join(hourlyTimes, ","), strings.Join(hourlyTemps, ","), strings.Join(hourlyCodes, ","), strings.Join(hourlyPrecip, ","),
	)
	wxTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(wxJSON))
	}))
	defer wxTS.Close()

	ev1Start := now.Add(90 * time.Minute)
	ev1End := ev1Start.Add(60 * time.Minute)
	ev2Day := now.AddDate(0, 0, 1)
	ev3Start := now.AddDate(0, 0, 2)
	icsBody := strings.Join([]string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"BEGIN:VEVENT",
		"SUMMARY:Porada týmu KindleIOT",
		"LOCATION:Kancelář 3A",
		"DTSTART:" + ev1Start.Format("20060102T150405"),
		"DTEND:" + ev1End.Format("20060102T150405"),
		"END:VEVENT",
		"BEGIN:VEVENT",
		"SUMMARY:Svoz tříděného odpadu",
		"DTSTART;VALUE=DATE:" + ev2Day.Format("20060102"),
		"DTEND;VALUE=DATE:" + ev2Day.AddDate(0, 0, 1).Format("20060102"),
		"END:VEVENT",
		"BEGIN:VEVENT",
		"SUMMARY:Zubař – prohlídka",
		"LOCATION:Poliklinika Centrum",
		"DTSTART:" + ev3Start.Format("20060102") + "T093000",
		"DTEND:" + ev3Start.Format("20060102") + "T101500",
		"END:VEVENT",
		"END:VCALENDAR",
	}, "\r\n")
	calTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(icsBody))
	}))
	defer calTS.Close()

	cfg := &config.Config{
		MQTT: config.MQTTConfig{
			Broker: "tcp://127.0.0.1:1883",
		},
		Weather: config.WeatherConfig{
			Enabled:      true,
			LocationName: "Praha",
			Latitude:     50.0755,
			Longitude:    14.4378,
		},
		Calendar: config.CalendarConfig{
			Enabled: true,
			URLs:    []string{calTS.URL},
		},
		Schedules: []config.ScheduleConfig{
			{
				ID:       "sched_morning",
				Name:     "Ranní rozsvícení",
				Enabled:  true,
				Hour:     7,
				Minute:   30,
				WidgetID: "sw1",
				State:    "ON",
			},
		},
		Device: config.DeviceConfig{
			Title:  "Chytrý byt",
			Width:  600,
			Height: 800,
		},
		Pages: []config.PageConfig{
			{
				ID:      "main",
				Title:   "Přehled",
				Columns: 2,
				Widgets: []config.WidgetConfig{
					{
						ID:            "sw1",
						Type:          "switch",
						Label:         "Stojací lampa",
						Subtitle:      "Obývák",
						StateTopic:    "local/switch/sw1",
						ValueOn:       "ON",
						ValueOff:      "OFF",
						PayloadOn:     "ON",
						PayloadOff:    "OFF",
						ShowInStandby: true,
					},
					{
						ID:             "t1",
						Type:           "timer",
						Label:          "Odpočet lampy",
						TargetWidgetID: "sw1",
						TargetState:    "OFF",
						TimerPresets:   []int{5, 15, 30},
					},
					{
						ID:    "wx",
						Type:  "weather",
						Label: "Počasí venku",
						Span:  2,
					},
					{
						ID:    "cal",
						Type:  "calendar",
						Label: "Rodinný kalendář",
						Span:  2,
					},
				},
			},
		},
	}
	if err := cfg.NormalizeAndValidate(); err != nil {
		t.Fatalf("NormalizeAndValidate failed: %v", err)
	}

	mq := mqtt.NewManager(cfg, nil, mqtt.CommandHandler{})
	mq.SetLocalValue("", "local/switch/sw1", "ON")
	mq.SetLocalValue("", "home/livingroom/temp", "22.4")

	hw := kindle.NewSystem()
	r, err := NewRenderer(cfg, mq, hw, Controller{})
	if err != nil {
		t.Fatalf("NewRenderer failed: %v", err)
	}

	wxSvc := weather.NewService(cfg.Weather, nil)
	wxSvc.SetEndpoint(wxTS.URL, wxTS.Client())
	wxSvc.FetchNow()
	r.SetWeatherService(wxSvc)

	calSvc := calendar.NewService(cfg.Calendar, nil)
	calSvc.SetHTTPClient(calTS.Client())
	calSvc.FetchNow()
	r.SetCalendarService(calSvc)

	// 1. Render initial frame and tap 5m timer preset -> verify active countdown & ui_timer.png
	_, _ = r.Render()
	tx, ty, ok := findHitCenter(r, ActionTimerStart)
	if !ok {
		t.Fatalf("expected ActionTimerStart hit region on timer card")
	}
	if !r.HandleTap(tx, ty) {
		t.Fatalf("expected HandleTap on timer preset to return true")
	}
	r.mu.Lock()
	ts, active := r.activeTimers["t1"]
	r.mu.Unlock()
	if !active {
		t.Fatalf("expected timer t1 to become active after preset tap")
	}
	imgTimer, _ := r.Render()
	maybeSavePreview("ui_timer.png", imgTimer)

	// Advance time past timer expiry -> should flip sw1 to OFF and return changed+wokeAlert
	changed, woke := r.TickTimersAndSchedules(ts.EndsAt.Add(2 * time.Second))
	if !changed || !woke {
		t.Fatalf("expected expired timer TickTimersAndSchedules to return changed=true woke=true, got %v %v", changed, woke)
	}
	if tv, ok := mq.GetTopic("", "local/switch/sw1"); !ok || tv.Payload != "OFF" {
		t.Fatalf("expected timer expiry to turn sw1 OFF, got %+v", tv)
	}

	// 2. Test Daily Schedule at 07:30 -> turns sw1 back ON once, not twice in same minute
	t0730 := time.Date(2026, 11, 23, 7, 30, 5, 0, time.Local)
	sChanged, sWoke := r.TickTimersAndSchedules(t0730)
	if !sChanged || sWoke {
		t.Fatalf("expected 07:30 schedule to fire cleanly without waking screen, got changed=%v woke=%v", sChanged, sWoke)
	}
	if tv, ok := mq.GetTopic("", "local/switch/sw1"); !ok || tv.Payload != "ON" {
		t.Fatalf("expected 07:30 schedule to turn sw1 ON, got %+v", tv)
	}
	if c2, _ := r.TickTimersAndSchedules(t0730.Add(20 * time.Second)); c2 {
		t.Fatalf("expected 07:30 schedule NOT to re-trigger in the same minute")
	}

	// 3. Open 7-day + 24h Weather Detail Modal -> ui_weather_detail.png
	_, _ = r.Render()
	wxX, wxY, ok := findHitCenter(r, ActionOpenWeatherDetail)
	if !ok {
		t.Fatalf("expected ActionOpenWeatherDetail hit region on weather card")
	}
	if !r.HandleTap(wxX, wxY) {
		t.Fatalf("expected weather card tap to open weather modal")
	}
	imgWxDetail, _ := r.Render()
	maybeSavePreview("ui_weather_detail.png", imgWxDetail)
	cx, cy, ok := findHitCenter(r, ActionCloseDetail)
	if !ok || !r.HandleTap(cx, cy) {
		t.Fatalf("expected close button tap on weather detail modal")
	}
	_, _ = r.Render()

	// 4. Open Calendar Detail Modal -> ui_calendar_detail.png
	calX, calY, ok := findHitCenter(r, ActionOpenCalendarDetail)
	if !ok {
		t.Fatalf("expected ActionOpenCalendarDetail hit region on calendar card")
	}
	if !r.HandleTap(calX, calY) {
		t.Fatalf("expected calendar card tap to open calendar modal")
	}
	imgCalDetail, _ := r.Render()
	maybeSavePreview("ui_calendar_detail.png", imgCalDetail)
	cx, cy, ok = findHitCenter(r, ActionCloseDetail)
	if !ok || !r.HandleTap(cx, cy) {
		t.Fatalf("expected close button tap on calendar detail modal")
	}

	// 5. Switch to Landscape (800x600, 3 columns) + Dark Mode -> ui_landscape_dark.png & ui_standby_landscape.png
	cfgLand := &config.Config{
		MQTT:     cfg.MQTT,
		Weather:  cfg.Weather,
		Calendar: cfg.Calendar,
		Device: config.DeviceConfig{
			Title:       "Chytrý byt",
			Width:       600,
			Height:      800,
			Orientation: "landscape",
			ThemeMode:   "dark",
		},
		Pages: []config.PageConfig{
			{
				ID:      "main",
				Title:   "Přehled",
				Columns: 3,
				Widgets: []config.WidgetConfig{
					cfg.Pages[0].Widgets[0], // sw1 (1 col)
					cfg.Pages[0].Widgets[1], // t1 (1 col)
					{
						ID:            "living_temp",
						Type:          "sensor",
						Label:         "Obývák",
						StateTopic:    "home/livingroom/temp",
						Unit:          "°C",
						ShowInStandby: true,
					},
					{
						ID:            "power_gauge",
						Type:          "gauge",
						Label:         "Příkon",
						StateTopic:    "home/livingroom/temp",
						Min:           0,
						Max:           50,
						Unit:          "°C",
						ShowInStandby: true,
					},
					cfg.Pages[0].Widgets[2], // weather (span 2)
					{
						ID:    "cal",
						Type:  "calendar",
						Label: "Kalendář",
						Span:  3,
					},
				},
			},
		},
	}
	if err := cfgLand.NormalizeAndValidate(); err != nil {
		t.Fatalf("cfgLand NormalizeAndValidate failed: %v", err)
	}
	r.UpdateConfig(cfgLand)
	imgLand, _ := r.Render()
	if imgLand.Bounds().Dx() != 800 || imgLand.Bounds().Dy() != 600 {
		t.Fatalf("expected 800x600 landscape canvas, got %v", imgLand.Bounds())
	}
	if bg := imgLand.GrayAt(5, 5).Y; bg >= 60 {
		t.Fatalf("expected dark mode inverted background (< 60), got Y=%d", bg)
	}
	maybeSavePreview("ui_landscape_dark.png", imgLand)

	// Landscape Standby screen (also render light vs dark — check light landscape standby for clean inspection or keep dark; plan says with dark mode or standby landscape)
	cfgLandLight := *cfgLand
	cfgLandLight.Device.ThemeMode = "light"
	r.UpdateConfig(&cfgLandLight)
	if !r.SetStandby(true) {
		t.Fatalf("expected SetStandby(true) to transition to landscape standby")
	}
	imgStandbyLand, _ := r.Render()
	if imgStandbyLand.Bounds().Dx() != 800 || imgStandbyLand.Bounds().Dy() != 600 {
		t.Fatalf("expected 800x600 landscape standby canvas, got %v", imgStandbyLand.Bounds())
	}
	maybeSavePreview("ui_standby_landscape.png", imgStandbyLand)
}
