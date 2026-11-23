package homekit

import (
	"testing"

	"kindleiot/internal/config"
	"kindleiot/internal/mqtt"
)

func TestHomeKitBridgeExposesOnlySelectedWidgetsAndPersistsState(t *testing.T) {
	storeDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.HomeKit.Enabled = true
	cfg.HomeKit.StoreDir = storeDir
	cfg.HomeKit.Port = 0

	// Opt-in only one switch ("lamp") to Apple Home.
	for i := range cfg.Pages[0].Widgets {
		if cfg.Pages[0].Widgets[i].ID == "lamp" {
			cfg.Pages[0].Widgets[i].HomeKit = true
			cfg.Pages[0].Widgets[i].HomeKitType = "lightbulb"
		}
	}

	if err := cfg.NormalizeAndValidate(); err != nil {
		t.Fatalf("NormalizeAndValidate: %v", err)
	}

	var br *Bridge
	mq := mqtt.NewManager(cfg, func() {
		if br != nil {
			br.SyncFromState()
		}
	}, mqtt.CommandHandler{})
	br = NewBridge(cfg, mq, nil)
	if err := br.Start(); err != nil {
		t.Fatalf("Start HomeKit bridge failed: %v", err)
	}

	st := br.Status()
	if !st.Enabled || !st.Running || st.Accessories != 1 {
		t.Fatalf("expected exactly 1 selected accessory in HomeKit bridge, got %+v", st)
	}

	// Simulate Kindle switch toggle and verify SyncFromState updates HomeKit characteristic and persists to disk.
	mq.SetLocalValue("", "stat/lamp/POWER", "ON")
	br.SyncFromState()

	if len(br.switches) != 1 || br.switches[0].widget.ID != "lamp" || !br.switches[0].onChar.Value() {
		t.Fatalf("expected lamp widget On=true after SyncFromState")
	}

	// Verify Reconfigure with unchanged HomeKit accessories keeps the exact same server instance alive.
	origSrv := br.server
	br.Reconfigure(cfg)
	if br.server != origSrv {
		t.Fatalf("expected Reconfigure with unchanged HomeKit widgets to keep existing HAP server alive")
	}

	// Simulate remote toggle from Apple Home -> Kindle state update.
	br.handleRemoteSwitchToggle(br.switches[0].widget.ID, false)
	if tv, ok := mq.GetTopic("", "stat/lamp/POWER"); !ok || tv.Payload != "OFF" {
		t.Fatalf("expected remote HomeKit toggle to update state to OFF, got %+v", tv)
	}

	// Toggle back to ON, stop bridge, and verify a fresh Bridge+Manager restores ON from disk on restart.
	mq.SetLocalValue("", "stat/lamp/POWER", "ON")
	br.SyncFromState()
	br.Stop()

	var br2 *Bridge
	mq2 := mqtt.NewManager(cfg, func() {
		if br2 != nil {
			br2.SyncFromState()
		}
	}, mqtt.CommandHandler{})
	br2 = NewBridge(cfg, mq2, nil)
	if err := br2.Start(); err != nil {
		t.Fatalf("Restart HomeKit bridge failed: %v", err)
	}
	defer br2.Stop()

	if !br2.switches[0].onChar.Value() {
		t.Fatalf("expected restarted HomeKit bridge to restore persisted On=true state")
	}
	if tv, ok := mq2.GetTopic("", "stat/lamp/POWER"); !ok || tv.Payload != "ON" {
		t.Fatalf("expected restarted bridge to restore local topic state to ON, got %+v", tv)
	}
}
