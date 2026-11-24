package ui

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kindleiot/internal/calendar"
	"kindleiot/internal/checklist"
	"kindleiot/internal/config"
	"kindleiot/internal/imgfetch"
	"kindleiot/internal/kindle"
	"kindleiot/internal/mqtt"
	"kindleiot/internal/spot"
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
				{ID: "c1", Type: "chart", Label: "Teplota", StateTopic: "home/temp", Unit: "°C", Precision: config.IntPtr(1)},
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
	cfg.Device.Language = "cs"
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
					Precision:     config.IntPtr(1),
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
			Title:    "Chytrý byt",
			Width:    600,
			Height:   800,
			Language: "cs",
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
			Language:    "cs",
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

func newMockWeatherServer(now time.Time) *httptest.Server {
	baseHour := now.Truncate(time.Hour)
	var hourlyTimes, hourlyTemps, hourlyCodes, hourlyPrecip []string
	for i := 0; i < 24; i++ {
		ht := baseHour.Add(time.Duration(i) * time.Hour)
		hourlyTimes = append(hourlyTimes, fmt.Sprintf("%q", ht.Format("2006-01-02T15:04")))
		hourlyTemps = append(hourlyTemps, fmt.Sprintf("%.1f", 5.0+float64(i%12)*0.7))
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
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(wxJSON))
	}))
}

func TestRendererI18nEnglishAndCzech(t *testing.T) {
	now := time.Now()
	if enDate := formatDateLine(time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC), "en"); strings.Contains(enDate, "Klement") || !strings.Contains(enDate, "Monday") {
		t.Fatalf("expected English date without Czech nameday, got %q", enDate)
	}
	if csDate := formatDateLine(time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC), "cs"); !strings.Contains(csDate, "Klement") || !strings.Contains(csDate, "Pondělí") {
		t.Fatalf("expected Czech date with Klement nameday, got %q", csDate)
	}

	// Main EN dashboard preview
	cfgMain := config.DefaultConfig()
	cfgMain.Device.Language = "en"
	mqMain := mqtt.NewManager(cfgMain, nil, mqtt.CommandHandler{})
	mqMain.SetLocalValue("", "home/livingroom/sensor", `{"temperature": 22.7, "humidity": 46}`)
	mqMain.SetLocalValue("", "zigbee2mqtt/living_light", `{"state": "ON"}`)
	rMain, err := NewRenderer(cfgMain, mqMain, kindle.NewSystem(), Controller{})
	if err != nil {
		t.Fatalf("NewRenderer EN main failed: %v", err)
	}
	imgMain, _ := rMain.Render()
	maybeSavePreview("en_main.png", imgMain)

	// Weather + Calendar EN servers
	wxTS := newMockWeatherServer(now)
	defer wxTS.Close()

	ev1Start := now.Add(90 * time.Minute)
	ev1End := ev1Start.Add(60 * time.Minute)
	ev2Day := now.AddDate(0, 0, 1)
	ev3Start := now.AddDate(0, 0, 2)
	icsBody := strings.Join([]string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"BEGIN:VEVENT",
		"SUMMARY:Team Standup Sync",
		"LOCATION:Room 3A",
		"DTSTART:" + ev1Start.Format("20060102T150405"),
		"DTEND:" + ev1End.Format("20060102T150405"),
		"END:VEVENT",
		"BEGIN:VEVENT",
		"SUMMARY:Recycling Pickup",
		"DTSTART;VALUE=DATE:" + ev2Day.Format("20060102"),
		"DTEND;VALUE=DATE:" + ev2Day.AddDate(0, 0, 1).Format("20060102"),
		"END:VEVENT",
		"BEGIN:VEVENT",
		"SUMMARY:Dentist Checkup",
		"LOCATION:City Clinic",
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
		MQTT:    config.MQTTConfig{Broker: "tcp://127.0.0.1:1883"},
		Weather: config.WeatherConfig{Enabled: true, LocationName: "Prague", Latitude: 50.0755, Longitude: 14.4378},
		Calendar: config.CalendarConfig{
			Enabled: true,
			URLs:    []string{calTS.URL},
		},
		Schedules: []config.ScheduleConfig{
			{ID: "morning_on", Name: "Morning Light", Enabled: true, Hour: 7, Minute: 30, WidgetID: "sw1", State: "ON"},
		},
		Device: config.DeviceConfig{
			Title:    "Smart Home",
			Width:    600,
			Height:   800,
			Language: "en",
			PinCode:  "1234",
		},
		Pages: []config.PageConfig{
			{
				ID:      "overview",
				Title:   "Overview",
				Columns: 2,
				Widgets: []config.WidgetConfig{
					{ID: "sw1", Type: "switch", Label: "Floor Lamp", Subtitle: "Living Room", StateTopic: "local/switch/sw1", ValueOn: "ON", ValueOff: "OFF", PayloadOn: "ON", PayloadOff: "OFF", ShowInStandby: true},
					{ID: "t1", Type: "timer", Label: "Lamp Timer", TargetWidgetID: "sw1", TargetState: "OFF", TimerPresets: []int{5, 15, 30}},
					{ID: "wx", Type: "weather", Label: "Outdoor Weather", Span: 2},
					{ID: "cal", Type: "calendar", Label: "Family Calendar", Span: 2},
				},
			},
			{
				ID:      "charts",
				Title:   "Charts",
				Columns: 2,
				Widgets: []config.WidgetConfig{
					{ID: "g1", Type: "gauge", Label: "Power", StateTopic: "home/power", Min: 0, Max: 3000, Unit: "W"},
					{ID: "c1", Type: "chart", Label: "Temperature", StateTopic: "home/temp", Unit: "°C", Precision: config.IntPtr(1)},
				},
			},
			{
				ID:    "map",
				Title: "Floorplan",
				Type:  "floorplan",
				Floorplan: &config.FloorplanConfig{
					Shapes: []config.FloorShape{
						{ID: "r1", Kind: "room", Label: "Living Room", X: 50, Y: 50, W: 450, H: 400},
						{ID: "w1", Kind: "wall", X: 50, Y: 500, X2: 900, Y2: 500},
						{ID: "d1", Kind: "door", X: 400, Y: 500, X2: 500, Y2: 500},
						{ID: "txt1", Kind: "text", Label: "Ground Floor", X: 600, Y: 100},
					},
					Pins: []config.FloorPin{
						{ID: "p1", X: 250, Y: 250, Widget: config.WidgetConfig{ID: "fp_temp", Type: "sensor", Label: "Temp", StateTopic: "home/temp", Unit: "°C"}},
						{ID: "p2", X: 700, Y: 300, Widget: config.WidgetConfig{ID: "fp_light", Type: "switch", Label: "Light", StateTopic: "local/switch/sw1", ValueOn: "ON", ValueOff: "OFF"}},
					},
				},
			},
		},
	}
	if err := cfg.NormalizeAndValidate(); err != nil {
		t.Fatalf("NormalizeAndValidate EN failed: %v", err)
	}

	mq := mqtt.NewManager(cfg, nil, mqtt.CommandHandler{})
	mq.SetLocalValue("", "local/switch/sw1", "ON")
	mq.SetLocalValue("", "home/livingroom/temp", "22.4")
	mq.SetLocalValue("", "home/power", "1850")
	mq.SetLocalValue("", "home/temp", "20.2")
	mq.SetLocalValue("", "home/temp", "21.6")

	r, err := NewRenderer(cfg, mq, kindle.NewSystem(), Controller{})
	if err != nil {
		t.Fatalf("NewRenderer EN failed: %v", err)
	}
	wxSvc := weather.NewService(cfg.Weather, nil)
	wxSvc.SetEndpoint(wxTS.URL, wxTS.Client())
	wxSvc.FetchNow()
	r.SetWeatherService(wxSvc)

	calSvc := calendar.NewService(cfg.Calendar, nil)
	calSvc.SetHTTPClient(calTS.Client())
	calSvc.FetchNow()
	r.SetCalendarService(calSvc)

	_, _ = r.Render()
	tx, ty, ok := findHitCenter(r, ActionTimerStart)
	if !ok || !r.HandleTap(tx, ty) {
		t.Fatalf("expected EN timer preset tap to start timer")
	}
	imgTimer, _ := r.Render()
	maybeSavePreview("en_timer_calendar.png", imgTimer)

	r.mu.Lock()
	tm := r.activeTimers["t1"]
	r.mu.Unlock()
	if ch, woke := r.TickTimersAndSchedules(tm.EndsAt.Add(2 * time.Second)); !ch || !woke {
		t.Fatalf("expected EN timer expiry")
	}
	r.mu.Lock()
	noticeTimer := r.noticeText
	r.mu.Unlock()
	if !strings.HasPrefix(noticeTimer, "✓ Timer finished:") {
		t.Fatalf("expected English timer notice, got %q", noticeTimer)
	}
	t0730 := time.Date(2026, 11, 23, 7, 30, 5, 0, time.Local)
	if ch, _ := r.TickTimersAndSchedules(t0730); !ch {
		t.Fatalf("expected EN schedule fire")
	}
	r.mu.Lock()
	noticeSched := r.noticeText
	r.mu.Unlock()
	if !strings.HasPrefix(noticeSched, "● Schedule triggered:") {
		t.Fatalf("expected English schedule notice, got %q", noticeSched)
	}

	// Open EN Weather Detail -> en_weather_detail.png
	_, _ = r.Render()
	wxX, wxY, _ := findHitCenter(r, ActionOpenWeatherDetail)
	r.HandleTap(wxX, wxY)
	imgWx, _ := r.Render()
	maybeSavePreview("en_weather_detail.png", imgWx)
	cx, cy, _ := findHitCenter(r, ActionCloseDetail)
	r.HandleTap(cx, cy)

	// Open EN Calendar Detail -> en_calendar_detail.png
	_, _ = r.Render()
	calX, calY, _ := findHitCenter(r, ActionOpenCalendarDetail)
	r.HandleTap(calX, calY)
	imgCal, _ := r.Render()
	maybeSavePreview("en_calendar_detail.png", imgCal)
	cx, cy, _ = findHitCenter(r, ActionCloseDetail)
	r.HandleTap(cx, cy)

	// Charts & Floorplan & PIN EN previews
	r.mu.Lock()
	r.activePage = 1
	r.mu.Unlock()
	imgCharts, _ := r.Render()
	maybeSavePreview("en_charts.png", imgCharts)

	r.mu.Lock()
	r.activePage = 2
	r.mu.Unlock()
	imgFP, _ := r.Render()
	maybeSavePreview("en_floorplan.png", imgFP)

	r.mu.Lock()
	r.pinModalOpen = true
	r.mu.Unlock()
	imgPin, _ := r.Render()
	maybeSavePreview("en_pin.png", imgPin)
	r.mu.Lock()
	r.pinModalOpen = false
	r.mu.Unlock()

	// Landscape Dark Mode + Standby EN previews
	cfgLand := &config.Config{
		MQTT:     cfg.MQTT,
		Weather:  cfg.Weather,
		Calendar: cfg.Calendar,
		Device: config.DeviceConfig{
			Title:       "Smart Home",
			Width:       600,
			Height:      800,
			Language:    "en",
			Orientation: "landscape",
			ThemeMode:   "dark",
		},
		Pages: []config.PageConfig{
			{
				ID:      "overview",
				Title:   "Overview",
				Columns: 3,
				Widgets: []config.WidgetConfig{
					cfg.Pages[0].Widgets[0],
					cfg.Pages[0].Widgets[1],
					{ID: "living_temp", Type: "sensor", Label: "Living Room", StateTopic: "home/livingroom/temp", Unit: "°C", ShowInStandby: true},
					{ID: "power_gauge", Type: "gauge", Label: "Power", StateTopic: "home/livingroom/temp", Min: 0, Max: 50, Unit: "°C", ShowInStandby: true},
					cfg.Pages[0].Widgets[2],
					{ID: "cal", Type: "calendar", Label: "Calendar", Span: 3},
				},
			},
		},
	}
	if err := cfgLand.NormalizeAndValidate(); err != nil {
		t.Fatalf("cfgLand EN NormalizeAndValidate failed: %v", err)
	}
	r.UpdateConfig(cfgLand)
	imgLand, _ := r.Render()
	maybeSavePreview("en_landscape_dark.png", imgLand)

	cfgLandLight := *cfgLand
	cfgLandLight.Device.ThemeMode = "light"
	r.UpdateConfig(&cfgLandLight)
	r.SetStandby(true)
	imgStandby, _ := r.Render()
	maybeSavePreview("en_standby_landscape.png", imgStandby)
}

func TestFormatNumberAndSensorPrecision(t *testing.T) {
	if got := formatNumber(20.771865844726562, -1); got != "20.771865844726562" {
		t.Fatalf("auto float = %q", got)
	}
	if got := formatNumber(22.0, -1); got != "22" {
		t.Fatalf("auto int = %q, want 22", got)
	}
	if got := formatNumber(28.369140625, 0); got != "28" {
		t.Fatalf("precision 0 = %q, want 28", got)
	}
	if got := formatNumber(20.771865844726562, 1); got != "20.8" {
		t.Fatalf("precision 1 = %q, want 20.8", got)
	}
	if got := formatNumber(20.771865844726562, 2); got != "20.77" {
		t.Fatalf("precision 2 = %q, want 20.77", got)
	}

	rawTemp := "20.771865844726562"
	rawHum := "28.369140625"

	wdAuto := config.WidgetConfig{Type: "sensor", Unit: "°C", SecondaryUnit: "%"}
	if got := formatWidgetValue(rawTemp, wdAuto); got != "20.771865844726562 °C" {
		t.Fatalf("auto sensor = %q", got)
	}
	if got := formatSecondaryValue(rawHum, wdAuto); got != "28.369140625 %" {
		t.Fatalf("auto secondary = %q", got)
	}

	wdPrec := config.WidgetConfig{
		Type:               "sensor",
		Precision:          config.IntPtr(1),
		Unit:               "°C",
		SecondaryPrecision: config.IntPtr(0),
		SecondaryUnit:      "%",
	}
	if got := formatWidgetValue(rawTemp, wdPrec); got != "20.8 °C" {
		t.Fatalf("precision 1 sensor = %q, want 20.8 °C", got)
	}
	if got := formatSecondaryValue(rawHum, wdPrec); got != "28 %" {
		t.Fatalf("secondary precision 0 = %q, want 28 %%", got)
	}
}

func TestRendererHiddenPagesAndOrdering(t *testing.T) {
	cfg := &config.Config{
		MQTT: config.MQTTConfig{Broker: "tcp://127.0.0.1:1883"},
		Device: config.DeviceConfig{
			Title:       "Panel",
			Width:       600,
			Height:      800,
			Language:    "en",
			Orientation: "portrait",
		},
		Pages: []config.PageConfig{
			{
				ID:      "p0",
				Title:   "Hidden First",
				Hidden:  true,
				Columns: 2,
				Widgets: []config.WidgetConfig{
					{ID: "w0", Type: "sensor", Label: "Secret", StateTopic: "t/secret", Unit: "°C"},
				},
			},
			{
				ID:      "p1",
				Title:   "Visible One",
				Columns: 2,
				Widgets: []config.WidgetConfig{
					{ID: "w1", Type: "sensor", Label: "Room", StateTopic: "t/room", Unit: "°C"},
				},
			},
			{
				ID:      "p2",
				Title:   "Visible Two",
				Columns: 2,
				Widgets: []config.WidgetConfig{
					{ID: "w2", Type: "switch", Label: "Lamp", StateTopic: "local/switch/w2", ValueOn: "ON", ValueOff: "OFF"},
				},
			},
		},
	}
	if err := cfg.NormalizeAndValidate(); err != nil {
		t.Fatalf("NormalizeAndValidate failed: %v", err)
	}
	mq := mqtt.NewManager(cfg, nil, mqtt.CommandHandler{})
	r, err := NewRenderer(cfg, mq, &kindle.System{}, Controller{})
	if err != nil {
		t.Fatalf("NewRenderer failed: %v", err)
	}

	// Render should auto-select first visible page (index 1) and show only tabs [1, 2].
	img, _ := r.Render()
	if img == nil {
		t.Fatalf("expected non-nil image")
	}
	if r.activePage != 1 {
		t.Fatalf("expected activePage to skip hidden page 0 and resolve to 1, got %d", r.activePage)
	}
	var tabHits []HitAction
	for _, h := range r.hits {
		if h.Type == ActionSelectPage {
			tabHits = append(tabHits, h)
		}
	}
	if len(tabHits) != 2 || tabHits[0].PageIdx != 1 || tabHits[1].PageIdx != 2 {
		t.Fatalf("expected visible tab PageIdx [1, 2], got %+v", tabHits)
	}

	// Tap second visible tab -> switches to page 2.
	cx := (tabHits[1].Rect.Min.X + tabHits[1].Rect.Max.X) / 2
	cy := (tabHits[1].Rect.Min.Y + tabHits[1].Rect.Max.Y) / 2
	if !r.HandleTap(cx, cy) || r.activePage != 2 {
		t.Fatalf("expected tap on second visible tab to switch to page 2, activePage=%d", r.activePage)
	}

	// Hide remaining visible pages -> Render should handle gracefully without panic and emit 0 tab hits.
	cfgAllHidden := *cfg
	cfgAllHidden.Pages = append([]config.PageConfig(nil), cfg.Pages...)
	for i := range cfgAllHidden.Pages {
		cfgAllHidden.Pages[i].Hidden = true
	}
	r.UpdateConfig(&cfgAllHidden)
	imgEmpty, _ := r.Render()
	if imgEmpty == nil {
		t.Fatalf("expected non-nil image even when all pages hidden")
	}
	for _, h := range r.hits {
		if h.Type == ActionSelectPage {
			t.Fatalf("expected zero tab hits when all pages are hidden, got %+v", h)
		}
	}
}

func TestRenderBLEPresenceWidgetWithBluetoothIcon(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Device.Language = "cs"
	cfg.Pages = []config.PageConfig{
		{
			ID:      "p1",
			Title:   "Přehled",
			Columns: 2,
			Widgets: []config.WidgetConfig{
				{
					ID:             "ble_pres",
					Type:           "sensor",
					Label:          "Přítomnost",
					Subtitle:       "Bluetooth",
					Icon:           "bluetooth",
					StateTopic:     "local/ble/presence",
					ValueMap:       map[string]string{"ON": "Blízko", "OFF": "Pryč"},
					SecondaryTopic: "local/ble/rssi",
					SecondaryUnit:  " dBm",
					ShowInStandby:  true,
				},
			},
		},
	}
	if err := cfg.NormalizeAndValidate(); err != nil {
		t.Fatalf("NormalizeAndValidate failed: %v", err)
	}
	mq := mqtt.NewManager(cfg, nil, mqtt.CommandHandler{})
	mq.SetLocalValue("", "local/ble/presence", "ON")
	mq.SetLocalValue("", "local/ble/rssi", "-48")

	r, err := NewRenderer(cfg, mq, &kindle.System{}, Controller{})
	if err != nil {
		t.Fatalf("NewRenderer failed: %v", err)
	}
	img, _ := r.Render()
	if img == nil || img.Bounds().Dx() != 600 || img.Bounds().Dy() != 800 {
		t.Fatalf("unexpected rendered image bounds: %v", img.Bounds())
	}
	nonWhite := 0
	for _, px := range img.Pix {
		if px != 0xFF {
			nonWhite++
		}
	}
	if nonWhite == 0 {
		t.Fatalf("expected non-white rendered pixels on BLE presence card")
	}
}

func TestLayoutGrid2DPackingAndExplicitPlacement(t *testing.T) {
	// Scenario 1: 3x3 grid with one 2x3 block + three 1x1 tiles -> three tiles fill column 2 vertically.
	widgets := []config.WidgetConfig{
		{ID: "big", Type: "calendar", Label: "Big", Span: 2, RowSpan: 3},
		{ID: "t1", Type: "switch", Label: "T1", Span: 1},
		{ID: "t2", Type: "switch", Label: "T2", Span: 1},
		{ID: "t3", Type: "switch", Label: "T3", Span: 1},
	}
	placed, totalRows := layoutGrid(widgets, 3, 3)
	if totalRows != 3 {
		t.Fatalf("expected totalRows=3, got %d", totalRows)
	}
	if placed[0].col != 0 || placed[0].row != 0 || placed[0].span != 2 || placed[0].rowSpan != 3 {
		t.Fatalf("unexpected big placement: %+v", placed[0])
	}
	for i := 1; i <= 3; i++ {
		if placed[i].col != 2 || placed[i].row != i-1 {
			t.Fatalf("expected t%d at col=2 row=%d, got %+v", i, i-1, placed[i])
		}
	}

	// Scenario 2: explicit bottom-right 2x2 block + collision displacement + sub-page boundary skip.
	widgets2 := []config.WidgetConfig{
		{ID: "pin_br", Type: "chart", Label: "BottomRight", Span: 2, RowSpan: 2, GridCol: config.IntPtr(1), GridRow: config.IntPtr(1)},
		{ID: "collide", Type: "switch", Label: "Colliding", Span: 1, RowSpan: 1, GridCol: config.IntPtr(2), GridRow: config.IntPtr(2)},
		{ID: "tall_next", Type: "sensor", Label: "TallNext", Span: 1, RowSpan: 3},
	}
	placed2, totalRows2 := layoutGrid(widgets2, 3, 3)
	if placed2[0].col != 1 || placed2[0].row != 1 {
		t.Fatalf("expected pinned block at (1,1), got %+v", placed2[0])
	}
	if placed2[1].col != 0 || placed2[1].row != 0 {
		t.Fatalf("expected colliding tile displaced to first free cell (0,0), got %+v", placed2[1])
	}
	// Column 0 has row 1 and 2 free, not 3 contiguous rows inside sub-page 0 (0..2), so 1x3 pushes cleanly to row=3.
	if placed2[2].row != 3 || placed2[2].rowSpan != 3 {
		t.Fatalf("expected 1x3 card not to split across sub-page boundary (row=3), got %+v", placed2[2])
	}
	if totalRows2 != 6 {
		t.Fatalf("expected totalRows=6, got %d", totalRows2)
	}
}

func TestRenderStretchedBlocks3x3And2x3(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Calendar.Enabled = true
	cfg.Pages = []config.PageConfig{
		{
			ID:      "p3x3",
			Title:   "3x3",
			Columns: 3,
			Rows:    3,
			Widgets: []config.WidgetConfig{
				{ID: "cal23", Type: "calendar", Label: "Schedule", Span: 2, RowSpan: 3},
				{ID: "sw1", Type: "switch", Label: "Light", Span: 1},
				{ID: "sen1", Type: "sensor", Label: "Temp", StateTopic: "t/1", Unit: "°C", Span: 1},
				{ID: "wx1", Type: "weather", Label: "Weather", Span: 1},
			},
		},
		{
			ID:      "full3x3",
			Title:   "BigChart",
			Columns: 3,
			Rows:    3,
			Widgets: []config.WidgetConfig{
				{ID: "ch33", Type: "chart", Label: "Power", StateTopic: "t/pow", Unit: "W", Span: 3, RowSpan: 3},
			},
		},
	}
	if err := cfg.NormalizeAndValidate(); err != nil {
		t.Fatalf("NormalizeAndValidate failed: %v", err)
	}
	mq := mqtt.NewManager(cfg, nil, mqtt.CommandHandler{})
	mq.SetLocalValue("", "t/1", "21.4")
	mq.SetLocalValue("", "t/pow", "420")
	mq.SetLocalValue("", "t/pow", "510")

	r, err := NewRenderer(cfg, mq, &kindle.System{}, Controller{})
	if err != nil {
		t.Fatalf("NewRenderer failed: %v", err)
	}
	img1, _ := r.Render()
	if img1 == nil {
		t.Fatalf("expected non-nil 3x3 image")
	}

	// Tap Switch card at col=2, row=0 -> verifies full hit box was registered and switches state ON.
	swX, swY, ok := findHitCenter(r, ActionWidgetToggle)
	if !ok {
		t.Fatalf("expected hit rect for switch tile on 3x3 grid")
	}
	if swX < 380 || swY > 300 {
		t.Fatalf("expected col=2 row=0 switch coordinates in upper-right cell, got (%d,%d)", swX, swY)
	}
	if !r.HandleTap(swX, swY) {
		t.Fatalf("expected switch tap on 3x3 grid to succeed")
	}

	// Switch to page 1 containing full-screen 3x3 Chart and render cleanly.
	r.mu.Lock()
	r.activePage = 1
	r.mu.Unlock()
	img2, _ := r.Render()
	if img2 == nil {
		t.Fatalf("expected non-nil 3x3 chart image")
	}
}

func TestRendererChecklistSpotImageWebhooksAndPageSwitch(t *testing.T) {
	var (
		mu       sync.Mutex
		hookHits []string
	)
	imgRGBA := image.NewRGBA(image.Rect(0, 0, 32, 24))
	for y := 0; y < 24; y++ {
		for x := 0; x < 32; x++ {
			imgRGBA.SetRGBA(x, y, color.RGBA{R: uint8(x * 7), G: 120, B: 180, A: 255})
		}
	}
	var pngBuf bytes.Buffer
	_ = png.Encode(&pngBuf, imgRGBA)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/spot":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"hoursToday":[
					{"hour":0,"priceEur":100,"priceCZK":2500,"level":"low"},
					{"hour":1,"priceEur":120,"priceCZK":3000,"level":"medium"},
					{"hour":12,"priceEur":220,"priceCZK":5500,"level":"high"}
				],
				"hoursTomorrow":[]
			}`))
		case "/img.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBuf.Bytes())
		case "/hook":
			mu.Lock()
			hookHits = append(hookHits, r.Method+" "+r.URL.RawQuery)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	cfg := &config.Config{
		MQTT: config.MQTTConfig{Broker: "tcp://127.0.0.1:1883"},
		SpotPrice: config.SpotPriceConfig{
			Enabled:  true,
			Currency: "CZK",
		},
		Device: config.DeviceConfig{
			Title:    "Test",
			Width:    600,
			Height:   800,
			Language: "cs",
		},
		Pages: []config.PageConfig{
			{
				ID:      "p_new",
				Title:   "Novinky",
				Columns: 2,
				Rows:    2,
				Widgets: []config.WidgetConfig{
					{
						ID:    "chk_ui",
						Type:  "checklist",
						Label: "Nákup",
						ChecklistItems: []config.ChecklistItem{
							{ID: "c1", Text: "Rohlíky", Done: false},
							{ID: "c2", Text: "Máslo", Done: true},
						},
					},
					{
						ID:    "spot_ui",
						Type:  "spot_price",
						Label: "Spot OTE",
					},
					{
						ID:       "img_ui",
						Type:     "image",
						Label:    "Radar",
						ImageURL: srv.URL + "/img.png",
					},
					{
						ID:         "btn_hook",
						Type:       "button",
						Label:      "Webhook",
						WebhookURL: srv.URL + "/hook?s={{state_lower}}",
					},
				},
			},
			{
				ID:      "p_second",
				Title:   "Druhá",
				Columns: 2,
				Widgets: []config.WidgetConfig{
					{
						ID:         "sw_hook",
						Type:       "switch",
						Label:      "Zásuvka",
						WebhookURL: srv.URL + "/hook?s={{state_lower}}",
					},
				},
			},
		},
	}
	if err := cfg.NormalizeAndValidate(); err != nil {
		t.Fatalf("NormalizeAndValidate failed: %v", err)
	}

	mq := mqtt.NewManager(cfg, nil, mqtt.CommandHandler{})
	chkStore := checklist.NewStore(filepath.Join(t.TempDir(), "chk.json"), nil)
	chkStore.SyncFromConfig(cfg)

	spotSvc := spot.NewService(cfg.SpotPrice, nil)
	spotSvc.SetBaseURL(srv.URL + "/spot")
	spotSvc.SetHTTPClient(srv.Client())
	spotSvc.FetchNow()

	imgSvc := imgfetch.NewService(cfg, nil)
	imgSvc.SetHTTPClient(srv.Client())
	imgSvc.Start()
	defer imgSvc.Stop()

	// Wait briefly for initial async image fetch
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if g, _, _ := imgSvc.GetRendered("img_ui", 100, 100, "contain", true); g != nil {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}

	r, err := NewRenderer(cfg, mq, &kindle.System{}, Controller{})
	if err != nil {
		t.Fatal(err)
	}
	r.SetChecklistStore(chkStore)
	r.SetSpotService(spotSvc)
	r.SetImageService(imgSvc)
	r.httpClient = srv.Client()

	img1, _ := r.Render()
	maybeSavePreview("ui_new_widgets.png", img1)
	if img1 == nil {
		t.Fatal("expected non-nil frame")
	}

	// Toggle first checklist row via tap
	tx, ty, ok := findHitCenter(r, ActionChecklistToggle)
	if !ok || !r.HandleTap(tx, ty) {
		t.Fatalf("expected checklist toggle tap hit")
	}
	if !chkStore.Items("chk_ui")[0].Done {
		t.Fatalf("expected c1 toggled to Done=true")
	}

	// Open & close Spot Price detail modal
	sx, sy, ok := findHitCenter(r, ActionOpenSpotDetail)
	if !ok || !r.HandleTap(sx, sy) {
		t.Fatalf("expected spot card tap hit")
	}
	imgModal, _ := r.Render()
	maybeSavePreview("ui_spot_modal.png", imgModal)
	cx, cy, ok := findHitCenter(r, ActionCloseDetail)
	if !ok || !r.HandleTap(cx, cy) {
		t.Fatalf("expected close detail hit inside spot modal")
	}
	_, _ = r.Render()

	// Trigger webhook button & switch via TriggerWidget
	if !r.TriggerWidget("btn_hook", "PRESS") || !r.TriggerWidget("sw_hook", "ON") {
		t.Fatalf("expected TriggerWidget to trigger webhook button & switch")
	}
	time.Sleep(80 * time.Millisecond)
	mu.Lock()
	gotHooks := append([]string(nil), hookHits...)
	mu.Unlock()
	if len(gotHooks) < 2 {
		t.Fatalf("expected 2 webhook HTTP hits, got %v", gotHooks)
	}

	// Switch page via selector ("Druhá", then "1")
	pid, ptitle, ok := r.SetActivePageBySelector("Druhá")
	if !ok || pid != "p_second" || ptitle != "Druhá" {
		t.Fatalf("SetActivePageBySelector('Druhá') failed: ok=%v pid=%q title=%q", ok, pid, ptitle)
	}
	pid2, _, ok2 := r.SetActivePageBySelector("1")
	if !ok2 || pid2 != "p_new" {
		t.Fatalf("SetActivePageBySelector('1') failed: ok=%v pid=%q", ok2, pid2)
	}
}
