package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"image"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"kindleiot/internal/ble"
	"kindleiot/internal/calendar"
	"kindleiot/internal/checklist"
	"kindleiot/internal/config"
	"kindleiot/internal/homekit"
	"kindleiot/internal/httppoll"
	"kindleiot/internal/imgfetch"
	"kindleiot/internal/kindle"
	"kindleiot/internal/mqtt"
	"kindleiot/internal/rules"
	"kindleiot/internal/spot"
	"kindleiot/internal/ui"
	"kindleiot/internal/weather"
	"kindleiot/internal/web"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "Path to YAML configuration file")
	headless := flag.Bool("headless", false, "Run without X11 window (web preview + MQTT only)")
	fullscreen := flag.Bool("fullscreen", false, "Request X11 fullscreen on desktop")
	flag.Parse()

	cfg, err := loadOrInitConfig(*cfgPath)
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	hw := kindle.NewSystem()
	go hw.KeepWiFiAwake()
	if cfg.Device.PreventSleep {
		hw.SetPreventScreenSaver(true)
	}
	if cfg.Device.InitialBrightness >= 0 {
		hw.SetBrightness(cfg.Device.InitialBrightness)
	} else if cfg.Device.BacklightTimeoutSec > 0 || cfg.Device.NightModeEnabled {
		hw.SetBrightness(cfg.Device.ActiveBrightnessForHour(time.Now().Hour()))
	}
	defer hw.SetPreventScreenSaver(false)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	redrawCh := make(chan bool, 16) // bool = force full e-ink flash
	requestRedraw := func(flash bool) {
		select {
		case redrawCh <- flash:
		default:
		}
	}

	var renderer *ui.Renderer
	var mq *mqtt.Manager
	var hkBridge *homekit.Bridge
	var checkStore *checklist.Store
	var spotSvc *spot.Service
	var imgSvc *imgfetch.Service
	var pollSvc *httppoll.Service
	var rulesEngine *rules.Engine

	var (
		powerMu         sync.Mutex
		lastActivity    = time.Now()
		backlightDimmed = false
	)
	wakeBacklight := func() {
		now := time.Now()
		powerMu.Lock()
		lastActivity = now
		wasDimmed := backlightDimmed
		backlightDimmed = false
		powerMu.Unlock()

		curCfg := cfg
		if renderer != nil {
			curCfg = renderer.Config()
		}
		target := curCfg.Device.ActiveBrightnessForHour(now.Hour())
		if wasDimmed || (hw.GetBrightness() == 0 && target > 0) {
			hw.SetBrightness(target)
		}
	}

	mq = mqtt.NewManager(
		cfg,
		func() {
			if hkBridge != nil {
				hkBridge.SyncFromState()
			}
			if renderer != nil {
				if alerts := renderer.EvaluateAlerts(); len(alerts) > 0 {
					wakeBacklight()
					requestRedraw(true)
					return
				}
			}
			requestRedraw(false)
		},
		mqtt.CommandHandler{
			OnBrightness: func(level int) {
				powerMu.Lock()
				lastActivity = time.Now()
				backlightDimmed = (level == 0)
				powerMu.Unlock()
				hw.SetBrightness(level)
				requestRedraw(false)
			},
			OnRefresh: func() {
				requestRedraw(true)
			},
			OnNotify: func(text string) {
				if renderer != nil && text != "" {
					renderer.SetStandby(false)
					wakeBacklight()
					renderer.ShowNotice(text, 15*time.Second)
					requestRedraw(false)
				}
			},
			OnTopicMessage: func(brokerID, topic, payload string) {
				if imgSvc != nil {
					imgSvc.HandleTopicMessage(brokerID, topic, payload)
				}
				if checkStore != nil && renderer != nil {
					curCfg := renderer.Config()
					primaryID := curCfg.PrimaryBrokerID()
					matchBroker := func(wb string) bool {
						if wb == "" {
							wb = primaryID
						}
						return brokerID == "" || brokerID == wb
					}
					text := strings.TrimSpace(payload)
					if text != "" {
						for _, p := range curCfg.Pages {
							for _, w := range p.Widgets {
								if w.Type == "checklist" && strings.TrimSpace(w.ChecklistAddTopic) == topic && matchBroker(w.BrokerID) {
									_, _ = checkStore.Add(w.ID, text)
								}
							}
							if p.Floorplan != nil {
								for _, pin := range p.Floorplan.Pins {
									w := pin.Widget
									if w.Type == "checklist" && strings.TrimSpace(w.ChecklistAddTopic) == topic && matchBroker(w.BrokerID) {
										_, _ = checkStore.Add(w.ID, text)
									}
								}
							}
						}
					}
				}
				if rulesEngine != nil {
					rulesEngine.HandleTopic(time.Now(), brokerID, topic, payload)
				}
			},
		},
	)

	keepAwake := cfg.Device.PreventSleep
	renderer, err = ui.NewRenderer(cfg, mq, hw, ui.Controller{
		OnQuit: func() {
			cancel()
		},
		OnFullRefresh: func() {
			requestRedraw(true)
		},
		OnBrightness: func(delta int) {
			cur := hw.GetBrightness()
			if cur < 0 {
				cur = 12
			}
			hw.SetBrightness(cur + delta)
			requestRedraw(false)
		},
		OnToggleAwake: func() {
			keepAwake = !keepAwake
			hw.SetPreventScreenSaver(keepAwake)
			requestRedraw(false)
		},
		OnWidgetChanged: func() {
			if hkBridge != nil {
				hkBridge.SyncFromState()
			}
		},
	})
	if err != nil {
		log.Fatalf("renderer init error: %v", err)
	}
	weatherSvc := weather.NewService(cfg.Weather, func() { requestRedraw(false) })
	renderer.SetWeatherService(weatherSvc)
	defer weatherSvc.Stop()

	calSvc := calendar.NewService(cfg.Calendar, func() { requestRedraw(false) })
	renderer.SetCalendarService(calSvc)
	defer calSvc.Stop()

	checkStore = checklist.NewStore(filepath.Join(filepath.Dir(*cfgPath), "checklists.json"), func() { requestRedraw(false) })
	checkStore.SyncFromConfig(cfg)
	renderer.SetChecklistStore(checkStore)

	publishLocalSpot := func(snap spot.Snapshot) {
		if !snap.Available || snap.Current == nil {
			return
		}
		mq.SetLocalValue("", "local/spot/price_czk", strconv.FormatFloat(snap.Current.PriceCZK, 'f', 2, 64))
		mq.SetLocalValue("", "local/spot/price_eur", strconv.FormatFloat(snap.Current.PriceEUR, 'f', 2, 64))
		mq.SetLocalValue("", "local/spot/level", snap.Current.Level)
		if raw, merr := json.Marshal(snap); merr == nil {
			mq.SetLocalValue("", "local/spot/json", string(raw))
		}
	}
	spotSvc = spot.NewService(cfg.SpotPrice, func(snap spot.Snapshot) {
		publishLocalSpot(snap)
		requestRedraw(false)
	})
	renderer.SetSpotService(spotSvc)
	defer spotSvc.Stop()

	imgSvc = imgfetch.NewService(cfg, func() { requestRedraw(false) })
	renderer.SetImageService(imgSvc)
	defer imgSvc.Stop()

	pollSvc = httppoll.NewService(cfg, mq)
	defer pollSvc.Stop()

	webhookClient := httppoll.NewHTTPClient(8 * time.Second)
	rulesEngine = rules.NewEngine(cfg, rules.Executor{
		ExecuteWidget: func(widgetID, state string) {
			if renderer.TriggerWidget(widgetID, state) {
				requestRedraw(false)
			}
		},
		PublishMQTT: func(brokerID, topic, payload string) {
			_ = mq.Publish(brokerID, topic, 0, false, payload)
		},
		FireWebhook: func(method, rawURL, body string) {
			go func() {
				wctx, wcancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer wcancel()
				if werr := httppoll.FireWebhook(wctx, webhookClient, method, rawURL, body, nil, ""); werr != nil {
					log.Printf("rule webhook error (%s): %v", rawURL, werr)
				}
			}()
		},
		SwitchPage: func(sel string) bool {
			if _, _, ok := renderer.SetActivePageBySelector(sel); ok {
				wakeBacklight()
				requestRedraw(true)
				return true
			}
			return false
		},
		ShowNotify: func(text string, dur time.Duration) {
			if text != "" {
				renderer.SetStandby(false)
				wakeBacklight()
				renderer.ShowNotice(text, dur)
				requestRedraw(false)
			}
		},
		SetBrightness: func(lvl int) {
			powerMu.Lock()
			lastActivity = time.Now()
			backlightDimmed = (lvl == 0)
			powerMu.Unlock()
			hw.SetBrightness(lvl)
			requestRedraw(false)
		},
		SetStandby: func(active bool) {
			if renderer.SetStandby(active) {
				if !active {
					wakeBacklight()
				}
				requestRedraw(true)
			}
		},
	})

	var xwin *ui.X11Window
	if !*headless {
		wmTitle := "L:A_N:application_PC:N_ID:org.kindleiot.panel_O:U"
		winW, winH := cfg.Device.LogicalSize()
		if hw.IsKindle() {
			short, long := cfg.Device.Width, cfg.Device.Height
			if short > long {
				short, long = long, short
			}
			winW, winH = short, long
		}
		xwin, err = ui.OpenX11Window(winW, winH, wmTitle, *fullscreen || hw.IsKindle())
		if err != nil {
			log.Fatalf("x11 init error: %v", err)
		}
		if hw.IsKindle() {
			xwin.SetOrientation(cfg.Device.Orientation)
		}
		defer xwin.Close()
	}

	hkBridge = homekit.NewBridge(cfg, mq, func() { requestRedraw(false) })
	if cfg.HomeKit.Enabled {
		hw.AllowUDPPort("5353")
		hw.AllowTCPPort(strconv.Itoa(cfg.HomeKit.Port))
		if hkErr := hkBridge.Start(); hkErr != nil {
			log.Printf("homekit start warning: %v", hkErr)
		}
	}
	defer hkBridge.Stop()

	var (
		latestMu  sync.RWMutex
		latestImg *image.Gray
	)
	setLatest := func(img *image.Gray) {
		latestMu.Lock()
		latestImg = img
		latestMu.Unlock()
	}
	getLatest := func() *image.Gray {
		latestMu.RLock()
		defer latestMu.RUnlock()
		return latestImg
	}

	// Render initial frame immediately before waiting on network I/O.
	drawFrame := func(forceFlash bool) {
		img, autoFlash := renderer.Render()
		setLatest(img)
		if xwin != nil {
			if forceFlash || autoFlash {
				_ = xwin.FlashAndDraw(img)
			} else {
				_ = xwin.Draw(img)
			}
		}
	}
	drawFrame(false)

	weatherSvc.Start()
	calSvc.Start()
	spotSvc.Start()
	imgSvc.Start()
	pollSvc.Start()
	go mq.Connect()
	defer mq.Disconnect()

	var (
		bleLocalMu       sync.Mutex
		lastBLELocalAt   time.Time
		lastBLELocalRSSI int
		lastBLELocalDev  string
	)
	updateLocalBLE := func(present bool, deviceName string, rssi int) {
		state := "OFF"
		dev := "-"
		rssiStr := ""
		if present {
			state = "ON"
			if deviceName != "" {
				dev = deviceName
			}
			rssiStr = strconv.Itoa(rssi)
		}
		mq.SetLocalValue("", "local/ble/presence", state)
		mq.SetLocalValue("", "local/ble/device", dev)
		mq.SetLocalValue("", "local/ble/rssi", rssiStr)
		if raw, err := json.Marshal(struct {
			Present bool   `json:"present"`
			Device  string `json:"device,omitempty"`
			RSSI    int    `json:"rssi,omitempty"`
		}{
			Present: present,
			Device:  deviceName,
			RSSI:    rssi,
		}); err == nil {
			mq.SetLocalValue("", "local/ble/json", string(raw))
		}
	}
	if cfg.Bluetooth.Enabled {
		updateLocalBLE(false, "", 0)
	}

	bleSvc := ble.NewService(cfg.Bluetooth, hw.IsKindle(), ble.Callbacks{
		OnPresenceChange: func(present bool, deviceName string, rssi int) {
			curCfg := renderer.Config()
			bleLocalMu.Lock()
			lastBLELocalAt = time.Now()
			lastBLELocalRSSI = rssi
			lastBLELocalDev = deviceName
			bleLocalMu.Unlock()
			updateLocalBLE(present, deviceName, rssi)

			if hkBridge != nil {
				hkBridge.SetOccupancy(present)
			}
			if rulesEngine != nil {
				rulesEngine.HandleBLEPresence(time.Now(), present, deviceName)
			}
			if curCfg.Bluetooth.PublishMQTT {
				mq.PublishPresence(present, deviceName, rssi)
			}
			if present {
				woke := false
				if curCfg.Bluetooth.WakeStandby && renderer.IsStandby() {
					if renderer.SetStandby(false) {
						woke = true
					}
				}
				if curCfg.Bluetooth.WakeBacklight {
					wakeBacklight()
				}
				if woke {
					requestRedraw(true)
				}
			}
		},
		OnProximityPing: func(deviceName string, rssi int) {
			now := time.Now()
			bleLocalMu.Lock()
			deltaRSSI := rssi - lastBLELocalRSSI
			if deltaRSSI < 0 {
				deltaRSSI = -deltaRSSI
			}
			shouldLocalUpdate := deviceName != lastBLELocalDev || deltaRSSI >= 4 || now.Sub(lastBLELocalAt) >= 10*time.Second
			if shouldLocalUpdate {
				lastBLELocalAt = now
				lastBLELocalRSSI = rssi
				lastBLELocalDev = deviceName
			}
			bleLocalMu.Unlock()
			if shouldLocalUpdate {
				updateLocalBLE(true, deviceName, rssi)
			}

			curCfg := renderer.Config()
			if !curCfg.Bluetooth.KeepAwake {
				return
			}
			powerMu.Lock()
			lastActivity = now
			dimmed := backlightDimmed
			powerMu.Unlock()
			if curCfg.Bluetooth.WakeStandby && renderer.IsStandby() {
				if renderer.SetStandby(false) {
					if curCfg.Bluetooth.WakeBacklight {
						wakeBacklight()
					}
					requestRedraw(true)
				}
			} else if curCfg.Bluetooth.WakeBacklight && dimmed {
				wakeBacklight()
			}
		},
	})
	bleSvc.Start()
	defer bleSvc.Stop()

	var webPort string
	if cfg.Web.Listen != "" {
		if _, p, splitErr := net.SplitHostPort(cfg.Web.Listen); splitErr == nil {
			webPort = p
			hw.AllowTCPPort(webPort)
		}
		webSrv := web.NewServer(*cfgPath, cfg, mq, hw, web.Hooks{
			GetImage: getLatest,
			HandleTap: func(x, y int) {
				wakeBacklight()
				if renderer.HandleTap(x, y) {
					requestRedraw(false)
				}
			},
			ReloadConfig: func(newCfg *config.Config) {
				if newCfg.HomeKit.Enabled {
					hw.AllowUDPPort("5353")
					hw.AllowTCPPort(strconv.Itoa(newCfg.HomeKit.Port))
				}
				renderer.UpdateConfig(newCfg)
				if xwin != nil && hw.IsKindle() {
					xwin.SetOrientation(newCfg.Device.Orientation)
				}
				mq.Reconfigure(newCfg)
				hkBridge.Reconfigure(newCfg)
				if newCfg.Bluetooth.Enabled {
					if _, ok := mq.GetTopic("", "local/ble/presence"); !ok {
						updateLocalBLE(false, "", 0)
					}
				}
				bleSvc.Reconfigure(newCfg.Bluetooth)
				weatherSvc.Reconfigure(newCfg.Weather)
				calSvc.Reconfigure(newCfg.Calendar)
				checkStore.SyncFromConfig(newCfg)
				spotSvc.Reconfigure(newCfg.SpotPrice)
				imgSvc.Reconfigure(newCfg)
				pollSvc.Reconfigure(newCfg)
				rulesEngine.Reconfigure(newCfg)
				wakeBacklight()
				requestRedraw(true)
			},
			HomeKitStatus: func() homekit.Status {
				return hkBridge.Status()
			},
			ResetHomeKit: func() error {
				return hkBridge.ResetPairings()
			},
			BLEStatus: func() ble.Status {
				return bleSvc.Status()
			},
			ApplyOTA: func(data []byte) error {
				exePath, exeErr := os.Executable()
				if exeErr != nil {
					return exeErr
				}
				if resolved, rerr := filepath.EvalSymlinks(exePath); rerr == nil {
					exePath = resolved
				}
				staged, stErr := web.WriteStagedBinary(exePath, data)
				if stErr != nil {
					return stErr
				}
				return web.SpawnOTARestart(exePath, staged, os.Args[1:])
			},
			Notify: func(text string, dur time.Duration) {
				if renderer != nil && text != "" {
					renderer.SetStandby(false)
					wakeBacklight()
					renderer.ShowNotice(text, dur)
					requestRedraw(false)
				}
			},
			SwitchPage: func(sel string) (string, string, bool) {
				pid, title, ok := renderer.SetActivePageBySelector(sel)
				if ok {
					wakeBacklight()
					requestRedraw(true)
				}
				return pid, title, ok
			},
			SetStandby: func(mode string) bool {
				var target bool
				switch strings.ToLower(strings.TrimSpace(mode)) {
				case "true", "on", "1":
					target = true
				case "false", "off", "0":
					target = false
				default:
					target = !renderer.IsStandby()
				}
				if renderer.SetStandby(target) {
					if !target {
						wakeBacklight()
					}
					requestRedraw(true)
				}
				return renderer.IsStandby()
			},
			SetBrightness: func(lvl int) {
				powerMu.Lock()
				lastActivity = time.Now()
				backlightDimmed = (lvl == 0)
				powerMu.Unlock()
				hw.SetBrightness(lvl)
				requestRedraw(false)
			},
			TriggerWidget: func(widgetID, state string) bool {
				ok := renderer.TriggerWidget(widgetID, state)
				if ok {
					requestRedraw(false)
				}
				return ok
			},
			ChecklistStore: checkStore,
			SpotStatus: func() spot.Snapshot {
				return spotSvc.Snapshot()
			},
			BatteryGuardStatus: func() rules.BatteryGuardStatus {
				return rulesEngine.BatteryStatus()
			},
			RulesLog: func() []rules.ExecutionLog {
				return rulesEngine.RecentLogs()
			},
			RefreshImage: func(widgetID string) {
				imgSvc.TriggerFetch(widgetID)
			},
		})
		httpSrv := &http.Server{
			Addr:              cfg.Web.Listen,
			Handler:           webSrv.Handler(),
			ReadHeaderTimeout: 5 * time.Second,
		}
		go func() {
			log.Printf("Web config & preview listening on %s", cfg.Web.Listen)
			if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("web server error: %v", err)
			}
		}()
		defer func() {
			shutCtx, shutCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer shutCancel()
			_ = httpSrv.Shutdown(shutCtx)
		}()
	}

	if xwin != nil {
		evCh := xwin.Events()
		go func() {
			for ev := range evCh {
				if ev.Closed {
					cancel()
					return
				}
				if ev.IsTap {
					wakeBacklight()
					if renderer.HandleTap(ev.TapX, ev.TapY) {
						requestRedraw(false)
					}
				} else if ev.Redraw {
					requestRedraw(false)
				}
			}
		}()
	}

	clockTick := time.NewTicker(30 * time.Second)
	defer clockTick.Stop()

	idleTick := time.NewTicker(5 * time.Second)
	defer idleTick.Stop()

	teleTick := time.NewTicker(time.Duration(cfg.Device.TelemetryIntervalSec) * time.Second)
	defer teleTick.Stop()

	var lastBatteryCheck time.Time
	checkIdleAndSchedule := func() {
		now := time.Now()
		curCfg := renderer.Config()

		powerMu.Lock()
		idleSec := int(now.Sub(lastActivity).Seconds())
		dimmed := backlightDimmed
		if curCfg.Device.BacklightTimeoutSec > 0 && !dimmed && idleSec >= curCfg.Device.BacklightTimeoutSec {
			backlightDimmed = true
			powerMu.Unlock()
			hw.SetBrightness(0)
		} else {
			powerMu.Unlock()
			if curCfg.Device.BacklightTimeoutSec == 0 && curCfg.Device.NightModeEnabled {
				target := curCfg.Device.ActiveBrightnessForHour(now.Hour())
				if hw.GetBrightness() != target {
					hw.SetBrightness(target)
				}
			}
		}

		if changed, wokeAlert := renderer.TickTimersAndSchedules(now); wokeAlert {
			wakeBacklight()
			requestRedraw(true)
		} else if changed {
			requestRedraw(false)
		}

		if rulesEngine != nil {
			rulesEngine.TickTime(now)
			if curCfg.BatteryGuard.Enabled && (lastBatteryCheck.IsZero() || now.Sub(lastBatteryCheck) >= 30*time.Second) {
				lastBatteryCheck = now
				st := hw.ReadStatus()
				rulesEngine.EvaluateBattery(now, st.Battery, st.Charging)
			}
		}

		if curCfg.Device.StandbyTimeoutSec > 0 && !renderer.IsStandby() && idleSec >= curCfg.Device.StandbyTimeoutSec {
			if renderer.SetStandby(true) {
				requestRedraw(true)
			}
		}
	}

	publishTele := func() {
		hw.KeepWiFiAwake()
		if webPort != "" {
			hw.AllowTCPPort(webPort)
		}
		if hkSt := hkBridge.Status(); hkSt.Enabled && hkSt.Port > 0 {
			hw.AllowUDPPort("5353")
			hw.AllowTCPPort(strconv.Itoa(hkSt.Port))
		}
		st := hw.ReadStatus()
		mq.PublishTelemetry(st.Battery, st.Charging, st.IP, st.Brightness)
		if rulesEngine != nil {
			lastBatteryCheck = time.Now()
			rulesEngine.EvaluateBattery(lastBatteryCheck, st.Battery, st.Charging)
		}
	}
	publishTele()

	for {
		select {
		case <-ctx.Done():
			return
		case flash := <-redrawCh:
			// Coalesce burst updates within 60ms to avoid multiple e-ink refreshes.
			time.Sleep(60 * time.Millisecond)
		drain:
			for {
				select {
				case f2 := <-redrawCh:
					if f2 {
						flash = true
					}
				default:
					break drain
				}
			}
			drawFrame(flash)
		case <-clockTick.C:
			drawFrame(false)
		case <-idleTick.C:
			checkIdleAndSchedule()
		case <-teleTick.C:
			publishTele()
		}
	}
}

func loadOrInitConfig(path string) (*config.Config, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		cfg := config.DefaultConfig()
		if saveErr := config.Save(path, cfg); saveErr != nil {
			return nil, saveErr
		}
		return cfg, nil
	}
	return config.Load(path)
}
