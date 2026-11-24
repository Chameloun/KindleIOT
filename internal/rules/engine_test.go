package rules

import (
	"testing"
	"time"

	"kindleiot/internal/config"
)

func TestRulesEngineTriggersAndGuards(t *testing.T) {
	var (
		widgetCalls  []string
		mqttCalls    []string
		webhookCalls []string
		pageCalls    []string
		notices      []string
		brightVal    = -1
		standbyVal   *bool
	)

	from := 21
	to := 6
	cfg := &config.Config{
		MQTT: config.MQTTConfig{ID: "default", Broker: "tcp://127.0.0.1:1883"},
		Rules: []config.RuleConfig{
			{
				ID:          "r_night_ble",
				Name:        "Night arrival",
				Enabled:     true,
				Trigger:     "ble_near",
				DeviceName:  "Watch",
				HourFrom:    &from,
				HourTo:      &to,
				CooldownSec: 30,
				ActionType:  "widget",
				WidgetID:    "hall_light",
				State:       "ON",
			},
			{
				ID:            "r_door_open",
				Name:          "Door opened",
				Enabled:       true,
				Trigger:       "topic_eq",
				Topic:         "zigbee/door",
				JSONPath:      "$.contact",
				Value:         "false",
				CooldownSec:   1,
				ActionType:    "page",
				TargetPage:    "cameras",
			},
			{
				ID:          "r_power_hi",
				Name:        "High load",
				Enabled:     true,
				Trigger:     "topic_gt",
				Topic:       "home/power",
				Threshold:   3000,
				CooldownSec: 1,
				ActionType:  "notify",
				NotifyText:  "High consumption!",
			},
			{
				ID:          "r_morning",
				Name:        "Morning bright",
				Enabled:     true,
				Trigger:     "time",
				Hour:        7,
				Minute:      30,
				Days:        []int{2}, // Tuesday
				ActionType:  "brightness",
				Brightness:  16,
			},
			{
				ID:            "r_away_hook",
				Name:          "Away hook",
				Enabled:       true,
				Trigger:       "ble_away",
				CooldownSec:   1,
				ActionType:    "webhook",
				WebhookMethod: "POST",
				WebhookURL:    "http://local/away",
			},
		},
	}

	eng := NewEngine(cfg, Executor{
		ExecuteWidget: func(w, s string) { widgetCalls = append(widgetCalls, w+":"+s) },
		PublishMQTT:   func(b, tp, p string) { mqttCalls = append(mqttCalls, tp+"="+p) },
		FireWebhook:   func(m, u, b string) { webhookCalls = append(webhookCalls, m+" "+u) },
		SwitchPage:    func(s string) bool { pageCalls = append(pageCalls, s); return true },
		ShowNotify:    func(txt string, _ time.Duration) { notices = append(notices, txt) },
		SetBrightness: func(lvl int) { brightVal = lvl },
		SetStandby:    func(a bool) { standbyVal = &a },
	})
	_ = mqttCalls
	_ = standbyVal

	// Noon -> Night arrival must NOT fire outside 21..6 window
	noon := time.Date(2026, 11, 24, 12, 0, 0, 0, time.Local)
	eng.HandleBLEPresence(noon, true, "Apple Watch")
	if len(widgetCalls) != 0 {
		t.Fatalf("expected no fire at noon, got %v", widgetCalls)
	}

	// 23:00 -> wrong device ignored, Watch fires once, immediate repeat blocked by cooldown
	night := time.Date(2026, 11, 24, 23, 0, 0, 0, time.Local)
	eng.HandleBLEPresence(night, true, "iPhone")
	eng.HandleBLEPresence(night, true, "Apple Watch")
	eng.HandleBLEPresence(night.Add(5*time.Second), true, "Apple Watch")
	if len(widgetCalls) != 1 || widgetCalls[0] != "hall_light:ON" {
		t.Fatalf("expected single hall_light:ON fire, got %v", widgetCalls)
	}

	// topic_eq rising edge: true -> ignore, false -> fires page switch, repeat false -> no duplicate edge fire
	eng.HandleTopic(night, "default", "zigbee/door", `{"contact":false}`)
	eng.HandleTopic(night.Add(5*time.Second), "default", "zigbee/door", `{"contact":false}`)
	eng.HandleTopic(night.Add(6*time.Second), "default", "zigbee/door", `{"contact":true}`)
	eng.HandleTopic(night.Add(8*time.Second), "default", "zigbee/door", `{"contact":false}`)
	if len(pageCalls) != 2 || pageCalls[0] != "cameras" {
		t.Fatalf("expected 2 rising-edge page switches, got %v", pageCalls)
	}

	// topic_gt fires notify once
	eng.HandleTopic(night, "default", "home/power", "3250")
	eng.HandleTopic(night.Add(2*time.Second), "default", "home/power", "3500")
	if len(notices) != 1 {
		t.Fatalf("expected 1 notice on threshold cross, got %v", notices)
	}

	// 2026-11-24 is Tuesday (isoDay=2)
	tue730 := time.Date(2026, 11, 24, 7, 30, 0, 0, time.Local)
	eng.TickTime(tue730)
	eng.TickTime(tue730.Add(20 * time.Second)) // same minute must not double fire
	if brightVal != 16 {
		t.Fatalf("expected morning brightness 16, got %d", brightVal)
	}

	eng.HandleBLEPresence(night, false, "Apple Watch")
	if len(webhookCalls) != 1 || webhookCalls[0] != "POST http://local/away" {
		t.Fatalf("expected ble_away webhook call, got %v", webhookCalls)
	}
	if len(eng.RecentLogs()) < 4 {
		t.Fatalf("expected populated recent logs, got %d", len(eng.RecentLogs()))
	}
}

func TestBatteryGuardHysteresisAndRetry(t *testing.T) {
	var actions []string
	var mqttMsgs []string
	var hooks []string

	cfg := &config.Config{
		BatteryGuard: config.BatteryGuardConfig{
			Enabled:    true,
			MinPercent: 30,
			MaxPercent: 80,
			WidgetID:   "charger",
			Topic:      "charger/set",
			PayloadOn:  "ON",
			PayloadOff: "OFF",
			WebhookOn:  "http://plug/on",
			WebhookOff: "http://plug/off",
		},
	}

	eng := NewEngine(cfg, Executor{
		ExecuteWidget: func(w, s string) { actions = append(actions, w+":"+s) },
		PublishMQTT:   func(b, tp, p string) { mqttMsgs = append(mqttMsgs, tp+"="+p) },
		FireWebhook:   func(m, u, b string) { hooks = append(hooks, u) },
	})

	t0 := time.Date(2026, 11, 24, 10, 0, 0, 0, time.Local)
	// Middle band -> ok, no action
	eng.EvaluateBattery(t0, 55, false)
	if len(actions) != 0 || eng.BatteryStatus().State != "ok" {
		t.Fatalf("expected ok state at 55%%, got %+v actions=%v", eng.BatteryStatus(), actions)
	}

	// Drops to 30% -> turns ON charger
	eng.EvaluateBattery(t0.Add(1*time.Minute), 30, false)
	if len(actions) != 1 || actions[0] != "charger:ON" || len(mqttMsgs) != 1 || hooks[0] != "http://plug/on" {
		t.Fatalf("expected ON trigger at 30%%, actions=%v mqtt=%v hooks=%v", actions, mqttMsgs, hooks)
	}

	// 4 minutes later, still not charging -> cooldown holds retry until 5m
	eng.EvaluateBattery(t0.Add(4*time.Minute), 29, false)
	if len(actions) != 1 {
		t.Fatalf("expected no retry before 5 minutes, got %v", actions)
	}

	// 6 minutes later, still not charging -> retries ON!
	eng.EvaluateBattery(t0.Add(6*time.Minute), 29, false)
	if len(actions) != 2 || actions[1] != "charger:ON" {
		t.Fatalf("expected 5m retry ON, got %v", actions)
	}

	// Now charging up through 65% -> stays charging_requested (no OFF yet)
	eng.EvaluateBattery(t0.Add(15*time.Minute), 65, true)
	if len(actions) != 2 || eng.BatteryStatus().State != "charging_requested" {
		t.Fatalf("expected hysteresis keep-on at 65%%, got %v state=%s", actions, eng.BatteryStatus().State)
	}

	// Hits 80% -> cutoff OFF!
	eng.EvaluateBattery(t0.Add(25*time.Minute), 80, true)
	if len(actions) != 3 || actions[2] != "charger:OFF" || hooks[len(hooks)-1] != "http://plug/off" {
		t.Fatalf("expected OFF cutoff at 80%%, got actions=%v hooks=%v", actions, hooks)
	}
}
