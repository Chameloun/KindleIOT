package main

import (
	"context"
	"errors"
	"flag"
	"image"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"kindleiot/internal/calendar"
	"kindleiot/internal/config"
	"kindleiot/internal/homekit"
	"kindleiot/internal/kindle"
	"kindleiot/internal/mqtt"
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
	go mq.Connect()
	defer mq.Disconnect()

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
				weatherSvc.Reconfigure(newCfg.Weather)
				calSvc.Reconfigure(newCfg.Calendar)
				wakeBacklight()
				requestRedraw(true)
			},
			HomeKitStatus: func() homekit.Status {
				return hkBridge.Status()
			},
			ResetHomeKit: func() error {
				return hkBridge.ResetPairings()
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
