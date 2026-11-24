package rules

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"kindleiot/internal/config"
	"kindleiot/internal/mqtt"
)

// Executor connects triggered rules and Battery Guard decisions to Kindle hardware, UI, MQTT, and Webhooks.
type Executor struct {
	ExecuteWidget func(widgetID, state string)
	PublishMQTT   func(brokerID, topic, payload string)
	FireWebhook   func(method, url, body string)
	SwitchPage    func(selector string) bool
	ShowNotify    func(text string, dur time.Duration)
	SetBrightness func(level int)
	SetStandby    func(active bool)
}

// ExecutionLog records one recently executed automation rule.
type ExecutionLog struct {
	RuleID   string    `json:"rule_id"`
	RuleName string    `json:"rule_name"`
	Trigger  string    `json:"trigger"`
	Action   string    `json:"action"`
	At       time.Time `json:"at"`
}

// BatteryGuardStatus reports the current state of 24/7 wall-mount charger protection.
type BatteryGuardStatus struct {
	Enabled      bool      `json:"enabled"`
	Battery      int       `json:"battery"`
	Charging     bool      `json:"charging"`
	MinPercent   int       `json:"min_percent"`
	MaxPercent   int       `json:"max_percent"`
	State        string    `json:"state"` // disabled, unknown, ok, charging_requested, cutoff_requested
	LastAction   string    `json:"last_action,omitempty"`
	LastActionAt time.Time `json:"last_action_at,omitempty"`
}

// Engine evaluates local "When -> Then" automation rules and Battery Guard hysteresis.
type Engine struct {
	mu            sync.Mutex
	cfg           *config.Config
	primaryBroker string
	exec          Executor

	lastFired     map[string]time.Time
	lastTimeFired map[string]string
	topicMatched  map[string]bool
	logs          []ExecutionLog

	batteryStatus BatteryGuardStatus
	batteryWant   string // "", "ON", "OFF"
	lastBatActAt  time.Time
}

// NewEngine creates a local automation and battery guard engine.
func NewEngine(cfg *config.Config, exec Executor) *Engine {
	e := &Engine{
		cfg:           cfg,
		exec:          exec,
		lastFired:     make(map[string]time.Time),
		lastTimeFired: make(map[string]string),
		topicMatched:  make(map[string]bool),
	}
	e.syncConfigLocked(cfg)
	return e
}

// Reconfigure applies updated rules and battery guard parameters.
func (e *Engine) Reconfigure(cfg *config.Config) {
	e.mu.Lock()
	e.cfg = cfg
	e.syncConfigLocked(cfg)
	e.mu.Unlock()
}

func (e *Engine) syncConfigLocked(cfg *config.Config) {
	if cfg == nil {
		e.primaryBroker = "default"
		return
	}
	e.primaryBroker = cfg.PrimaryBrokerID()
	bg := cfg.BatteryGuard
	e.batteryStatus.Enabled = bg.Enabled
	e.batteryStatus.MinPercent = bg.MinPercent
	e.batteryStatus.MaxPercent = bg.MaxPercent
	if !bg.Enabled {
		e.batteryStatus.State = "disabled"
		e.batteryWant = ""
	} else if e.batteryStatus.State == "" || e.batteryStatus.State == "disabled" {
		e.batteryStatus.State = "ok"
		e.batteryStatus.Battery = -1
	}
}

func (e *Engine) recordLogLocked(r config.RuleConfig, now time.Time) {
	summary := r.ActionType
	switch r.ActionType {
	case "widget":
		summary = fmt.Sprintf("widget %s -> %s", r.WidgetID, r.State)
	case "page":
		summary = "page " + r.TargetPage
	case "notify":
		summary = "notify: " + r.NotifyText
	case "brightness":
		summary = fmt.Sprintf("brightness %d", r.Brightness)
	case "standby":
		summary = fmt.Sprintf("standby=%v", r.StandbyActive)
	}
	e.logs = append([]ExecutionLog{{
		RuleID:   r.ID,
		RuleName: r.Name,
		Trigger:  r.Trigger,
		Action:   summary,
		At:       now,
	}}, e.logs...)
	if len(e.logs) > 20 {
		e.logs = e.logs[:20]
	}
}

// HandleBLEPresence evaluates ble_near and ble_away automation rules.
func (e *Engine) HandleBLEPresence(now time.Time, present bool, deviceName string) {
	wantTrig := "ble_away"
	if present {
		wantTrig = "ble_near"
	}

	e.mu.Lock()
	var toRun []config.RuleConfig
	if e.cfg != nil {
		devLower := strings.ToLower(strings.TrimSpace(deviceName))
		for _, r := range e.cfg.Rules {
			if !r.Enabled || r.Trigger != wantTrig {
				continue
			}
			if filter := strings.ToLower(strings.TrimSpace(r.DeviceName)); filter != "" && !strings.Contains(devLower, filter) {
				continue
			}
			if !ruleScheduleAllows(r, now) || e.inCooldownLocked(r, now) {
				continue
			}
			e.lastFired[r.ID] = now
			e.recordLogLocked(r, now)
			toRun = append(toRun, r)
		}
	}
	exec := e.exec
	e.mu.Unlock()

	for _, r := range toRun {
		execRuleAction(exec, r)
	}
}

// HandleTopic evaluates topic_eq, topic_gt, and topic_lt automation rules with rising-edge tracking.
func (e *Engine) HandleTopic(now time.Time, brokerID, topic, payload string) {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return
	}

	e.mu.Lock()
	var toRun []config.RuleConfig
	if e.cfg != nil {
		for _, r := range e.cfg.Rules {
			if !r.Enabled || strings.TrimSpace(r.Topic) != topic {
				continue
			}
			if r.Trigger != "topic_eq" && r.Trigger != "topic_gt" && r.Trigger != "topic_lt" {
				continue
			}
			rBroker := strings.TrimSpace(r.BrokerID)
			if rBroker == "" {
				rBroker = e.primaryBroker
			}
			if brokerID != "" && brokerID != rBroker {
				continue
			}

			val := mqtt.ExtractValue(payload, r.JSONPath)
			matched := evalTopicCondition(r, val)
			wasMatched := e.topicMatched[r.ID]
			e.topicMatched[r.ID] = matched

			if !matched || wasMatched {
				continue
			}
			if !ruleScheduleAllows(r, now) || e.inCooldownLocked(r, now) {
				continue
			}
			e.lastFired[r.ID] = now
			e.recordLogLocked(r, now)
			toRun = append(toRun, r)
		}
	}
	exec := e.exec
	e.mu.Unlock()

	for _, r := range toRun {
		execRuleAction(exec, r)
	}
}

func evalTopicCondition(r config.RuleConfig, val string) bool {
	switch r.Trigger {
	case "topic_eq":
		return strings.EqualFold(strings.TrimSpace(val), strings.TrimSpace(r.Value))
	case "topic_gt":
		f, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
		return err == nil && f > r.Threshold
	case "topic_lt":
		f, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
		return err == nil && f < r.Threshold
	default:
		return false
	}
}

// TickTime evaluates time-based automation rules once per matching minute.
func (e *Engine) TickTime(now time.Time) {
	minuteKey := now.Format("2006-01-02 15:04")

	e.mu.Lock()
	var toRun []config.RuleConfig
	if e.cfg != nil {
		for _, r := range e.cfg.Rules {
			if !r.Enabled || r.Trigger != "time" {
				continue
			}
			if r.Hour != now.Hour() || r.Minute != now.Minute() {
				continue
			}
			if !ruleScheduleAllows(r, now) {
				continue
			}
			if e.lastTimeFired[r.ID] == minuteKey {
				continue
			}
			e.lastTimeFired[r.ID] = minuteKey
			e.lastFired[r.ID] = now
			e.recordLogLocked(r, now)
			toRun = append(toRun, r)
		}
	}
	if len(e.lastTimeFired) > 100 {
		for k, v := range e.lastTimeFired {
			if v != minuteKey {
				delete(e.lastTimeFired, k)
			}
		}
	}
	exec := e.exec
	e.mu.Unlock()

	for _, r := range toRun {
		execRuleAction(exec, r)
	}
}

// EvaluateBattery checks Kindle battery percentage and charging status against BatteryGuard thresholds.
func (e *Engine) EvaluateBattery(now time.Time, battery int, charging bool) {
	e.mu.Lock()
	if e.cfg == nil {
		e.mu.Unlock()
		return
	}
	bg := e.cfg.BatteryGuard
	e.batteryStatus.Enabled = bg.Enabled
	e.batteryStatus.Battery = battery
	e.batteryStatus.Charging = charging
	e.batteryStatus.MinPercent = bg.MinPercent
	e.batteryStatus.MaxPercent = bg.MaxPercent

	if !bg.Enabled {
		e.batteryStatus.State = "disabled"
		e.batteryWant = ""
		e.mu.Unlock()
		return
	}
	if battery < 0 {
		e.batteryStatus.State = "unknown"
		e.mu.Unlock()
		return
	}

	minPct := bg.MinPercent
	if minPct <= 0 {
		minPct = 30
	}
	maxPct := bg.MaxPercent
	if maxPct <= minPct {
		maxPct = 80
	}

	nextWant := e.batteryWant
	if battery <= minPct {
		nextWant = "ON"
	} else if battery >= maxPct {
		nextWant = "OFF"
	}

	if nextWant == "" {
		e.batteryStatus.State = "ok"
		e.mu.Unlock()
		return
	}

	if nextWant == "ON" {
		e.batteryStatus.State = "charging_requested"
	} else {
		e.batteryStatus.State = "cutoff_requested"
	}

	transition := nextWant != e.batteryWant
	mismatch := (nextWant == "ON" && !charging) || (nextWant == "OFF" && charging)
	retryDue := mismatch && (e.lastBatActAt.IsZero() || now.Sub(e.lastBatActAt) >= 5*time.Minute)

	shouldFire := transition || retryDue
	if shouldFire {
		e.batteryWant = nextWant
		e.lastBatActAt = now
		e.batteryStatus.LastAction = nextWant
		e.batteryStatus.LastActionAt = now
	}
	exec := e.exec
	e.mu.Unlock()

	if !shouldFire {
		return
	}

	if bg.WidgetID != "" && exec.ExecuteWidget != nil {
		exec.ExecuteWidget(bg.WidgetID, nextWant)
	}
	if strings.TrimSpace(bg.Topic) != "" && exec.PublishMQTT != nil {
		payload := bg.PayloadOff
		if nextWant == "ON" {
			payload = bg.PayloadOn
		}
		exec.PublishMQTT(bg.BrokerID, bg.Topic, payload)
	}
	if nextWant == "ON" && strings.TrimSpace(bg.WebhookOn) != "" && exec.FireWebhook != nil {
		exec.FireWebhook("", bg.WebhookOn, "")
	} else if nextWant == "OFF" && strings.TrimSpace(bg.WebhookOff) != "" && exec.FireWebhook != nil {
		exec.FireWebhook("", bg.WebhookOff, "")
	}
}

// BatteryStatus returns the current Battery Guard status snapshot.
func (e *Engine) BatteryStatus() BatteryGuardStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.batteryStatus
}

// RecentLogs returns up to 20 most recently triggered rule executions.
func (e *Engine) RecentLogs() []ExecutionLog {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]ExecutionLog, len(e.logs))
	copy(out, e.logs)
	return out
}

func (e *Engine) inCooldownLocked(r config.RuleConfig, now time.Time) bool {
	last, ok := e.lastFired[r.ID]
	if !ok {
		return false
	}
	cd := r.CooldownSec
	if cd <= 0 {
		cd = 10
	}
	return now.Sub(last) < time.Duration(cd)*time.Second
}

func ruleScheduleAllows(r config.RuleConfig, now time.Time) bool {
	if len(r.Days) > 0 {
		isoDay := int(now.Weekday())
		if isoDay == 0 {
			isoDay = 7
		}
		found := false
		for _, d := range r.Days {
			if d == isoDay {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return hourInWindow(now.Hour(), r.HourFrom, r.HourTo)
}

func hourInWindow(hour int, from, to *int) bool {
	if from == nil || to == nil {
		return true
	}
	f, t := *from, *to
	if f == t {
		return true
	}
	if f < t {
		return hour >= f && hour < t
	}
	return hour >= f || hour < t
}

func execRuleAction(exec Executor, r config.RuleConfig) {
	switch r.ActionType {
	case "widget":
		if exec.ExecuteWidget != nil && strings.TrimSpace(r.WidgetID) != "" {
			exec.ExecuteWidget(r.WidgetID, r.State)
		}
	case "mqtt":
		if exec.PublishMQTT != nil && strings.TrimSpace(r.ActionTopic) != "" {
			exec.PublishMQTT(r.ActionBrokerID, r.ActionTopic, r.ActionPayload)
		}
	case "webhook":
		if exec.FireWebhook != nil && strings.TrimSpace(r.WebhookURL) != "" {
			exec.FireWebhook(r.WebhookMethod, r.WebhookURL, r.WebhookBody)
		}
	case "page":
		if exec.SwitchPage != nil && strings.TrimSpace(r.TargetPage) != "" {
			exec.SwitchPage(r.TargetPage)
		}
	case "notify":
		if exec.ShowNotify != nil && strings.TrimSpace(r.NotifyText) != "" {
			exec.ShowNotify(r.NotifyText, 15*time.Second)
		}
	case "brightness":
		if exec.SetBrightness != nil {
			exec.SetBrightness(r.Brightness)
		}
	case "standby":
		if exec.SetStandby != nil {
			exec.SetStandby(r.StandbyActive)
		}
	}
}
