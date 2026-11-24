package homekit

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brutella/hap"
	"github.com/brutella/hap/accessory"
	"github.com/brutella/hap/characteristic"
	"github.com/brutella/hap/service"

	"kindleiot/internal/config"
	"kindleiot/internal/mqtt"
)

// Status describes the current Apple HomeKit Bridge state for the web UI.
type Status struct {
	Enabled     bool   `json:"enabled"`
	Running     bool   `json:"running"`
	Paired      bool   `json:"paired"`
	Pin         string `json:"pin"`
	Port        int    `json:"port"`
	Accessories int    `json:"accessories"`
}

type boundSwitch struct {
	widget config.WidgetConfig
	onChar *characteristic.On
}

type boundTemp struct {
	widget config.WidgetConfig
	acc    *accessory.Thermometer
}

// Bridge exposes user-selected KindleIOT widgets (where widget.HomeKit == true) to Apple Home via HAP.
type Bridge struct {
	mu            sync.Mutex
	cfg           *config.Config
	mq            *mqtt.Manager
	onUpdate      func()
	server        *hap.Server
	cancel        context.CancelFunc
	doneCh        chan struct{}
	switches      []boundSwitch
	temps         []boundTemp
	occupancy     *service.OccupancySensor
	lastOccupancy bool
	signature     string
}

// NewBridge creates a new HomeKit Bridge manager.
func NewBridge(cfg *config.Config, mq *mqtt.Manager, onUpdate func()) *Bridge {
	return &Bridge{
		cfg:      cfg,
		mq:       mq,
		onUpdate: onUpdate,
	}
}

type seedTopic struct {
	brokerID string
	topic    string
	val      string
}

// Start launches the HomeKit HAP server in the background if homekit.enabled is true.
func (b *Bridge) Start() error {
	b.mu.Lock()
	seeds, err := b.startLocked()
	b.mu.Unlock()
	b.applySeeds(seeds)
	return err
}

func (b *Bridge) applySeeds(seeds []seedTopic) {
	for _, s := range seeds {
		b.mq.SetLocalValue(s.brokerID, s.topic, s.val)
	}
}

func accessoryDisplayName(w config.WidgetConfig) string {
	lbl := strings.TrimSpace(w.Label)
	sub := strings.TrimSpace(w.Subtitle)
	if lbl == "" {
		lbl = w.ID
	}
	if sub != "" && !strings.Contains(strings.ToLower(lbl), strings.ToLower(sub)) {
		return lbl + " " + sub
	}
	return lbl
}

func buildHomeKitSignature(cfg *config.Config) string {
	if cfg == nil || !cfg.HomeKit.Enabled {
		return "disabled"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s|%d|%s|ble_occ=%v|", cfg.HomeKit.Pin, cfg.HomeKit.Port, cfg.HomeKit.StoreDir,
		cfg.Bluetooth.Enabled && cfg.Bluetooth.HomeKitOccupancy)
	seen := make(map[string]bool)
	addW := func(w config.WidgetConfig) {
		if !w.HomeKit || w.ID == "" || seen[w.ID] {
			return
		}
		seen[w.ID] = true
		fmt.Fprintf(&sb, "%s:%s:%s:%s;", w.ID, w.Type, w.HomeKitType, accessoryDisplayName(w))
	}
	for _, p := range cfg.Pages {
		for _, w := range p.Widgets {
			addW(w)
		}
		if p.Floorplan != nil {
			for _, pin := range p.Floorplan.Pins {
				addW(pin.Widget)
			}
		}
	}
	return sb.String()
}

func (b *Bridge) stateFilePath() string {
	dir := b.cfg.HomeKit.StoreDir
	if dir == "" {
		dir = "homekit-db"
	}
	return filepath.Join(dir, "widget_states.json")
}

func (b *Bridge) loadPersistedStates() map[string]bool {
	out := make(map[string]bool)
	data, err := os.ReadFile(b.stateFilePath())
	if err != nil {
		return out
	}
	_ = json.Unmarshal(data, &out)
	return out
}

func (b *Bridge) savePersistedStates(states map[string]bool) {
	p := b.stateFilePath()
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	data, err := json.Marshal(states)
	if err != nil {
		return
	}
	_ = os.WriteFile(p, data, 0o644)
}

func (b *Bridge) isWidgetOn(w config.WidgetConfig) (bool, bool) {
	if tv, ok := b.mq.GetTopic(w.BrokerID, w.StateTopic); ok {
		val := mqtt.ExtractValue(tv.Payload, w.StateJSONPath)
		return strings.EqualFold(val, w.ValueOn), true
	}
	return false, false
}

func (b *Bridge) startLocked() ([]seedTopic, error) {
	if !b.cfg.HomeKit.Enabled {
		b.signature = "disabled"
		return nil, nil
	}

	persisted := b.loadPersistedStates()
	var seeds []seedTopic

	bridgeAcc := accessory.NewBridge(accessory.Info{
		Name:         b.cfg.Device.Title + " Bridge",
		Manufacturer: "KindleIOT",
		Model:        "Kindle KT4",
		SerialNumber: "KINDLEIOT-BRIDGE-1",
	})
	bridgeAcc.A.Id = 1

	usedIDs := map[uint64]bool{1: true}
	allocID := func(widgetID string) uint64 {
		h := fnv.New32a()
		_, _ = h.Write([]byte(widgetID))
		id := uint64(h.Sum32()%1000000) + 2
		for usedIDs[id] {
			id++
		}
		usedIDs[id] = true
		return id
	}

	b.switches = nil
	b.temps = nil
	b.occupancy = nil
	var children []*accessory.A
	seenWidgets := make(map[string]bool)

	addWidget := func(w config.WidgetConfig) {
		if !w.HomeKit || w.ID == "" || seenWidgets[w.ID] {
			return
		}
		seenWidgets[w.ID] = true

		switch w.Type {
		case "switch":
			info := accessory.Info{
				Name:         accessoryDisplayName(w),
				Manufacturer: "KindleIOT",
				Model:        "Kindle Switch",
				SerialNumber: w.ID,
			}
			var rawAcc *accessory.A
			var onChar *characteristic.On
			switch w.HomeKitType {
			case "switch":
				sw := accessory.NewSwitch(info)
				rawAcc = sw.A
				onChar = sw.Switch.On
			case "outlet":
				out := accessory.NewOutlet(info)
				rawAcc = out.A
				onChar = out.Outlet.On
			default:
				lb := accessory.NewLightbulb(info)
				rawAcc = lb.A
				onChar = lb.Lightbulb.On
			}
			rawAcc.Id = allocID(w.ID)

			isOn, hasLive := b.isWidgetOn(w)
			if !hasLive {
				if savedOn, ok := persisted[w.ID]; ok {
					isOn = savedOn
				}
				if w.StateTopic != "" && w.StateJSONPath == "" {
					val := w.ValueOff
					if isOn {
						val = w.ValueOn
					}
					seeds = append(seeds, seedTopic{brokerID: w.BrokerID, topic: w.StateTopic, val: val})
				}
			}
			onChar.SetValue(isOn)

			wdID := w.ID
			onChar.OnValueRemoteUpdate(func(on bool) {
				b.handleRemoteSwitchToggle(wdID, on)
			})

			b.switches = append(b.switches, boundSwitch{widget: w, onChar: onChar})
			children = append(children, rawAcc)

		case "sensor", "gauge", "chart":
			acc := accessory.NewTemperatureSensor(accessory.Info{
				Name:         accessoryDisplayName(w),
				Manufacturer: "KindleIOT",
				Model:        "Temperature Sensor",
				SerialNumber: w.ID,
			})
			acc.A.Id = allocID(w.ID)
			acc.TempSensor.CurrentTemperature.SetMinValue(-50)
			acc.TempSensor.CurrentTemperature.SetMaxValue(100)

			if tv, ok := b.mq.GetTopic(w.BrokerID, w.StateTopic); ok {
				if f, err := strconv.ParseFloat(mqtt.ExtractValue(tv.Payload, w.StateJSONPath), 64); err == nil {
					acc.TempSensor.CurrentTemperature.SetValue(f)
				}
			}
			b.temps = append(b.temps, boundTemp{widget: w, acc: acc})
			children = append(children, acc.A)
		}
	}

	for _, p := range b.cfg.Pages {
		for _, w := range p.Widgets {
			addWidget(w)
		}
		if p.Floorplan != nil {
			for _, pin := range p.Floorplan.Pins {
				addWidget(pin.Widget)
			}
		}
	}

	if b.cfg.Bluetooth.Enabled && b.cfg.Bluetooth.HomeKitOccupancy {
		occAcc := accessory.New(accessory.Info{
			Name:         b.cfg.Device.Title + " Presence",
			Manufacturer: "KindleIOT",
			Model:        "BLE Proximity",
			SerialNumber: "KINDLEIOT-BLE-OCC",
		}, accessory.TypeSensor)
		occAcc.Id = allocID("__ble_occupancy__")
		occSvc := service.NewOccupancySensor()
		val := characteristic.OccupancyDetectedOccupancyNotDetected
		if b.lastOccupancy {
			val = characteristic.OccupancyDetectedOccupancyDetected
		}
		occSvc.OccupancyDetected.SetValue(val)
		occAcc.AddS(occSvc.S)
		b.occupancy = occSvc
		children = append(children, occAcc)
	}

	store := hap.NewFsStore(b.cfg.HomeKit.StoreDir)
	srv, err := hap.NewServer(store, bridgeAcc.A, children...)
	if err != nil {
		return nil, fmt.Errorf("init homekit server: %w", err)
	}
	srv.Pin = b.cfg.HomeKit.Pin
	srv.Addr = fmt.Sprintf(":%d", b.cfg.HomeKit.Port)

	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan struct{})
	b.server = srv
	b.cancel = cancel
	b.doneCh = doneCh
	b.signature = buildHomeKitSignature(b.cfg)

	go func() {
		defer close(doneCh)
		log.Printf("Apple HomeKit Bridge listening on %s (PIN: %s, selected accessories: %d)", srv.Addr, srv.Pin, len(children))
		if err := srv.ListenAndServe(ctx); err != nil && ctx.Err() == nil {
			log.Printf("HomeKit server stopped: %v", err)
		}
	}()
	return seeds, nil
}

func (b *Bridge) handleRemoteSwitchToggle(widgetID string, on bool) {
	b.mu.Lock()
	var wd config.WidgetConfig
	found := false
	states := make(map[string]bool, len(b.switches))
	for _, sw := range b.switches {
		if sw.widget.ID == widgetID {
			wd = sw.widget
			found = true
			states[sw.widget.ID] = on
		} else {
			states[sw.widget.ID] = sw.onChar.Value()
		}
	}
	b.mu.Unlock()
	if !found {
		return
	}

	nextState := wd.ValueOff
	cmdPayload := wd.PayloadOff
	if on {
		nextState = wd.ValueOn
		cmdPayload = wd.PayloadOn
	}
	log.Printf("HomeKit remote update from Apple Home: %s (%s) -> %s", accessoryDisplayName(wd), wd.ID, nextState)
	if wd.StateTopic != "" && wd.StateJSONPath == "" {
		b.mq.SetLocalValue(wd.BrokerID, wd.StateTopic, nextState)
	}
	b.savePersistedStates(states)

	if strings.TrimSpace(wd.CommandTopic) != "" {
		_ = b.mq.Publish(wd.BrokerID, wd.CommandTopic, wd.QoS, wd.Retain, cmdPayload)
	}
	if b.onUpdate != nil {
		b.onUpdate()
	}
}

// SyncFromState updates HomeKit accessory characteristics from current Kindle/MQTT state.
func (b *Bridge) SyncFromState() {
	b.mu.Lock()
	switches := append([]boundSwitch(nil), b.switches...)
	temps := append([]boundTemp(nil), b.temps...)
	b.mu.Unlock()

	states := make(map[string]bool, len(switches))
	for _, sw := range switches {
		isOn := sw.onChar.Value()
		if liveOn, ok := b.isWidgetOn(sw.widget); ok {
			isOn = liveOn
			if sw.onChar.Value() != isOn {
				log.Printf("HomeKit event push to Apple Home: %s (%s) -> %v", accessoryDisplayName(sw.widget), sw.widget.ID, isOn)
				sw.onChar.SetValue(isOn)
			}
		}
		states[sw.widget.ID] = isOn
	}
	if len(switches) > 0 {
		b.savePersistedStates(states)
	}

	for _, t := range temps {
		if tv, ok := b.mq.GetTopic(t.widget.BrokerID, t.widget.StateTopic); ok {
			raw := mqtt.ExtractValue(tv.Payload, t.widget.StateJSONPath)
			if f, err := strconv.ParseFloat(raw, 64); err == nil {
				if t.acc.TempSensor.CurrentTemperature.Value() != f {
					t.acc.TempSensor.CurrentTemperature.SetValue(f)
				}
			}
		}
	}
}

// Stop shuts down the HomeKit server cleanly.
func (b *Bridge) Stop() {
	b.mu.Lock()
	cancel := b.cancel
	doneCh := b.doneCh
	b.cancel = nil
	b.doneCh = nil
	b.server = nil
	b.mu.Unlock()

	if cancel != nil {
		cancel()
		if doneCh != nil {
			select {
			case <-doneCh:
			case <-time.After(2 * time.Second):
			}
		}
	}
}

// Reconfigure updates the HomeKit Bridge with new configuration.
// If the HomeKit accessory list did not structurally change, it keeps the existing HAP server
// and active Apple Home TCP event subscriptions alive without interruption.
func (b *Bridge) Reconfigure(newCfg *config.Config) {
	newSig := buildHomeKitSignature(newCfg)

	b.mu.Lock()
	if b.server != nil && b.signature == newSig {
		b.cfg = newCfg
		byID := make(map[string]config.WidgetConfig)
		for _, p := range newCfg.Pages {
			for _, w := range p.Widgets {
				if w.HomeKit && w.ID != "" {
					byID[w.ID] = w
				}
			}
			if p.Floorplan != nil {
				for _, pin := range p.Floorplan.Pins {
					w := pin.Widget
					if w.HomeKit && w.ID != "" {
						byID[w.ID] = w
					}
				}
			}
		}
		for i := range b.switches {
			if updated, ok := byID[b.switches[i].widget.ID]; ok {
				b.switches[i].widget = updated
			}
		}
		for i := range b.temps {
			if updated, ok := byID[b.temps[i].widget.ID]; ok {
				b.temps[i].widget = updated
			}
		}
		b.mu.Unlock()
		b.SyncFromState()
		return
	}
	b.mu.Unlock()

	b.Stop()
	b.mu.Lock()
	b.cfg = newCfg
	seeds, _ := b.startLocked()
	b.mu.Unlock()
	b.applySeeds(seeds)
}

// ResetPairings wipes stored HomeKit pairings on disk and restarts the bridge so a new iPhone/Home can pair.
func (b *Bridge) ResetPairings() error {
	b.Stop()
	b.mu.Lock()
	if b.cfg.HomeKit.StoreDir != "" {
		_ = os.RemoveAll(b.cfg.HomeKit.StoreDir)
	}
	seeds, err := b.startLocked()
	b.mu.Unlock()
	b.applySeeds(seeds)
	return err
}

// SetOccupancy updates the HomeKit OccupancySensor state for BLE proximity detection.
func (b *Bridge) SetOccupancy(present bool) {
	b.mu.Lock()
	b.lastOccupancy = present
	occ := b.occupancy
	b.mu.Unlock()
	if occ == nil {
		return
	}
	target := characteristic.OccupancyDetectedOccupancyNotDetected
	if present {
		target = characteristic.OccupancyDetectedOccupancyDetected
	}
	if occ.OccupancyDetected.Value() != target {
		occ.OccupancyDetected.SetValue(target)
	}
}

// Status returns current HomeKit pairing and runtime status.
func (b *Bridge) Status() Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	accCount := len(b.switches) + len(b.temps)
	if b.occupancy != nil {
		accCount++
	}
	st := Status{
		Enabled:     b.cfg.HomeKit.Enabled,
		Running:     b.server != nil,
		Pin:         b.cfg.HomeKit.Pin,
		Port:        b.cfg.HomeKit.Port,
		Accessories: accCount,
	}
	if b.server != nil {
		st.Paired = b.server.IsPaired()
	}
	return st
}
