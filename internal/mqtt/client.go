package mqtt

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"

	"kindleiot/internal/config"
)

// TopicValue stores the latest received payload for an MQTT topic.
type TopicValue struct {
	Payload   string    `json:"payload"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CommandHandler handles device-level MQTT commands directed at the Kindle itself.
type CommandHandler struct {
	OnBrightness func(level int)
	OnRefresh    func()
	OnNotify     func(text string)
}

// Client manages the universal MQTT connection, subscriptions, and topic state store.
type Client struct {
	mu         sync.RWMutex
	cfg        config.MQTTConfig
	client     paho.Client
	stopCh     chan struct{}
	connected  bool
	lastError  string
	values     map[string]TopicValue
	history    map[string][]TopicValue
	topics     []string
	onUpdate   func()
	cmdHandler CommandHandler
}

// NewClient initializes a new MQTT client wrapper without connecting yet.
func NewClient(cfg config.MQTTConfig, topics []string, onUpdate func(), cmd CommandHandler) *Client {
	return &Client{
		cfg:        cfg,
		values:     make(map[string]TopicValue),
		history:    make(map[string][]TopicValue),
		topics:     append([]string(nil), topics...),
		onUpdate:   onUpdate,
		cmdHandler: cmd,
	}
}

// NormalizeBrokerURL converts common MQTT URI formats (mqtt://, mqtts://, tls://, bare host)
// into a URI accepted by paho.mqtt.golang (tcp://, ssl://, ws://, wss://).
func NormalizeBrokerURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		if strings.HasSuffix(s, ":8883") || (!strings.Contains(s, ":") && strings.Contains(strings.ToLower(s), "hivemq.cloud")) {
			s = "ssl://" + s
		} else {
			s = "tcp://" + s
		}
	}
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "mqtt://"):
		s = "tcp://" + s[len("mqtt://"):]
	case strings.HasPrefix(lower, "mqtts://"):
		s = "ssl://" + s[len("mqtts://"):]
	case strings.HasPrefix(lower, "tls://"):
		s = "ssl://" + s[len("tls://"):]
	}

	u, err := url.Parse(s)
	if err != nil {
		return s
	}
	if u.Port() == "" && u.Hostname() != "" {
		switch u.Scheme {
		case "ssl":
			u.Host = u.Hostname() + ":8883"
		case "tcp":
			u.Host = u.Hostname() + ":1883"
		}
	}
	return u.String()
}

// Connect starts the MQTT connection loop with embedded root CAs and error reporting.
func (c *Client) Connect() error {
	c.mu.Lock()
	if c.stopCh != nil {
		close(c.stopCh)
	}
	stopCh := make(chan struct{})
	c.stopCh = stopCh
	cfg := c.cfg
	c.mu.Unlock()

	brokerURI := NormalizeBrokerURL(cfg.Broker)
	u, err := url.Parse(brokerURI)
	if err != nil {
		c.setError("invalid broker URL: " + err.Error())
		return err
	}

	opts := paho.NewClientOptions()
	opts.AddBroker(brokerURI)
	clientID := cfg.ClientID
	if clientID == "" {
		clientID = fmt.Sprintf("kindleiot-%d", time.Now().Unix()%10000)
	}
	opts.SetClientID(clientID)
	if cfg.Username != "" {
		opts.SetUsername(cfg.Username)
		opts.SetPassword(cfg.Password)
	}
	opts.SetKeepAlive(time.Duration(cfg.KeepAliveSec) * time.Second)
	opts.SetPingTimeout(10 * time.Second)
	opts.SetConnectTimeout(10 * time.Second)
	opts.SetAutoReconnect(true)
	opts.SetMaxReconnectInterval(60 * time.Second)
	opts.SetCleanSession(true)

	if u.Scheme == "ssl" || u.Scheme == "wss" || cfg.InsecureTLS {
		opts.SetTLSConfig(&tls.Config{
			RootCAs:            buildRootCAs(),
			ServerName:         u.Hostname(),
			InsecureSkipVerify: cfg.InsecureTLS, //nolint:gosec // user-configured self-signed local broker
			MinVersion:         tls.VersionTLS12,
		})
	}

	statusTopic := cfg.BaseTopic + "/status"
	opts.SetWill(statusTopic, "offline", 1, true)

	opts.SetOnConnectHandler(func(cl paho.Client) {
		c.mu.Lock()
		c.connected = true
		c.lastError = ""
		topics := append([]string(nil), c.topics...)
		c.mu.Unlock()

		log.Printf("MQTT connected to %s", brokerURI)
		cl.Publish(statusTopic, 1, true, "online")
		c.subscribeAll(cl, topics)
		if cfg.HomeAssistantDiscovery {
			c.publishDiscovery(cl)
		}
		if c.onUpdate != nil {
			c.onUpdate()
		}
	})

	opts.SetConnectionLostHandler(func(_ paho.Client, err error) {
		c.mu.Lock()
		c.connected = false
		if err != nil {
			c.lastError = err.Error()
		}
		c.mu.Unlock()
		log.Printf("MQTT connection lost: %v", err)
		if c.onUpdate != nil {
			c.onUpdate()
		}
	})

	opts.SetDefaultPublishHandler(func(_ paho.Client, msg paho.Message) {
		c.handleMessage(msg.Topic(), string(msg.Payload()))
	})

	cl := paho.NewClient(opts)
	c.mu.Lock()
	c.client = cl
	c.mu.Unlock()

	go c.connectLoop(cl, brokerURI, stopCh)
	return nil
}

func (c *Client) connectLoop(cl paho.Client, brokerURI string, stopCh <-chan struct{}) {
	for {
		select {
		case <-stopCh:
			return
		default:
		}
		tok := cl.Connect()
		if tok.WaitTimeout(10 * time.Second) {
			if err := tok.Error(); err == nil {
				return
			} else {
				log.Printf("MQTT connect to %s failed: %v", brokerURI, err)
				c.setError(err.Error())
			}
		} else {
			log.Printf("MQTT connect to %s timed out", brokerURI)
			c.setError("connect timeout (" + brokerURI + ")")
		}

		select {
		case <-stopCh:
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (c *Client) setError(msg string) {
	c.mu.Lock()
	c.connected = false
	c.lastError = msg
	c.mu.Unlock()
	if c.onUpdate != nil {
		c.onUpdate()
	}
}

// Reconfigure applies a new MQTT configuration, reconnecting if broker/auth/TLS changed.
func (c *Client) Reconfigure(newCfg config.MQTTConfig, topics []string) {
	c.mu.Lock()
	needsReconnect := !c.cfg.SameConnection(newCfg)
	c.cfg = newCfg
	c.topics = append([]string(nil), topics...)
	cl := c.client
	c.mu.Unlock()

	if !needsReconnect {
		if cl != nil && cl.IsConnected() {
			c.subscribeAll(cl, topics)
		}
		return
	}

	c.Disconnect()
	_ = c.Connect()
}

// Disconnect cleanly publishes offline LWT and closes the MQTT connection.
func (c *Client) Disconnect() {
	c.mu.Lock()
	if c.stopCh != nil {
		close(c.stopCh)
		c.stopCh = nil
	}
	cl := c.client
	c.client = nil
	c.connected = false
	base := c.cfg.BaseTopic
	c.mu.Unlock()

	if cl != nil {
		if cl.IsConnected() {
			tok := cl.Publish(base+"/status", 1, true, "offline")
			tok.WaitTimeout(500 * time.Millisecond)
		}
		cl.Disconnect(250)
	}
}

// UpdateSubscriptions refreshes subscribed widget topics after a config reload.
func (c *Client) UpdateSubscriptions(topics []string) {
	c.mu.Lock()
	c.topics = append([]string(nil), topics...)
	cl := c.client
	c.mu.Unlock()
	if cl != nil && cl.IsConnected() {
		c.subscribeAll(cl, topics)
	}
}

func (c *Client) subscribeAll(cl paho.Client, topics []string) {
	c.mu.RLock()
	base := c.cfg.BaseTopic
	c.mu.RUnlock()

	subs := make(map[string]byte, len(topics)+3)
	for _, t := range topics {
		if t != "" {
			subs[t] = 0
		}
	}
	subs[base+"/brightness/set"] = 0
	subs[base+"/screen/refresh"] = 0
	subs[base+"/notify"] = 0

	if len(subs) > 0 {
		cl.SubscribeMultiple(subs, func(_ paho.Client, msg paho.Message) {
			c.handleMessage(msg.Topic(), string(msg.Payload()))
		})
	}
}

func (c *Client) handleMessage(topic, payload string) {
	c.mu.RLock()
	base := c.cfg.BaseTopic
	cmd := c.cmdHandler
	c.mu.RUnlock()

	switch topic {
	case base + "/brightness/set":
		if v, err := strconv.Atoi(strings.TrimSpace(payload)); err == nil && cmd.OnBrightness != nil {
			cmd.OnBrightness(v)
		}
	case base + "/screen/refresh":
		if cmd.OnRefresh != nil {
			cmd.OnRefresh()
		}
	case base + "/notify":
		if cmd.OnNotify != nil {
			cmd.OnNotify(strings.TrimSpace(payload))
		}
	}

	c.recordValue(topic, payload)

	if c.onUpdate != nil {
		c.onUpdate()
	}
}

const maxTopicHistory = 120

func (c *Client) recordValue(topic, payload string) {
	tv := TopicValue{
		Payload:   payload,
		UpdatedAt: time.Now(),
	}
	c.mu.Lock()
	c.values[topic] = tv
	h := append(c.history[topic], tv)
	if len(h) > maxTopicHistory {
		h = h[len(h)-maxTopicHistory:]
	}
	c.history[topic] = h
	c.mu.Unlock()
}

// Publish sends an MQTT message to the given topic.
func (c *Client) Publish(topic string, qos byte, retain bool, payload string) error {
	if strings.TrimSpace(topic) == "" {
		return fmt.Errorf("empty command_topic")
	}
	c.mu.RLock()
	cl := c.client
	c.mu.RUnlock()
	if cl == nil || !cl.IsConnected() {
		return fmt.Errorf("mqtt not connected")
	}
	tok := cl.Publish(topic, qos, retain, payload)
	if !tok.WaitTimeout(3 * time.Second) {
		return fmt.Errorf("mqtt publish timeout")
	}
	return tok.Error()
}

// SetLocalValue updates the local topic cache optimistically.
func (c *Client) SetLocalValue(topic, payload string) {
	if topic == "" {
		return
	}
	c.recordValue(topic, payload)
	if c.onUpdate != nil {
		c.onUpdate()
	}
}

// HistoryPoint represents one numeric sample extracted from topic history.
type HistoryPoint struct {
	Value float64
	Time  time.Time
}

// GetHistory returns up to maxPoints numeric values extracted from recent payloads on topic.
func (c *Client) GetHistory(topic, jsonPath string, maxPoints int) []HistoryPoint {
	c.mu.RLock()
	raw := append([]TopicValue(nil), c.history[topic]...)
	c.mu.RUnlock()

	var pts []HistoryPoint
	for _, tv := range raw {
		s := ExtractValue(tv.Payload, jsonPath)
		if v, err := strconv.ParseFloat(s, 64); err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) {
			pts = append(pts, HistoryPoint{Value: v, Time: tv.UpdatedAt})
		}
	}
	if maxPoints > 0 && len(pts) > maxPoints {
		pts = pts[len(pts)-maxPoints:]
	}
	return pts
}

// GetTopic returns the latest cached value for a topic.
func (c *Client) GetTopic(topic string) (TopicValue, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.values[topic]
	return v, ok
}

// Snapshot returns connection status and all cached topic values for the web inspector.
func (c *Client) Snapshot() (connected bool, lastErr string, values map[string]TopicValue) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	copyMap := make(map[string]TopicValue, len(c.values))
	for k, v := range c.values {
		copyMap[k] = v
	}
	return c.connected, c.lastError, copyMap
}

// PublishTelemetry publishes Kindle battery, charging, and IP metadata to base_topic.
func (c *Client) PublishTelemetry(battery int, charging bool, ip string, brightness int) {
	c.mu.RLock()
	cl := c.client
	base := c.cfg.BaseTopic
	c.mu.RUnlock()
	if cl == nil || !cl.IsConnected() {
		return
	}
	if battery >= 0 {
		cl.Publish(base+"/battery", 0, true, strconv.Itoa(battery))
	}
	if charging {
		cl.Publish(base+"/charging", 0, true, "ON")
	} else {
		cl.Publish(base+"/charging", 0, true, "OFF")
	}
	if ip != "" {
		cl.Publish(base+"/ip", 0, true, ip)
	}
	if brightness >= 0 {
		cl.Publish(base+"/brightness", 0, true, strconv.Itoa(brightness))
	}
}

func (c *Client) publishDiscovery(cl paho.Client) {
	c.mu.RLock()
	cfg := c.cfg
	c.mu.RUnlock()

	nodeID := sanitizeID(cfg.ClientID)
	if nodeID == "" {
		nodeID = "kindleiot"
	}
	device := map[string]any{
		"identifiers":  []string{nodeID},
		"name":         "Kindle IoT Panel (" + nodeID + ")",
		"manufacturer": "Amazon / KindleIOT",
		"model":        "Kindle KT4",
	}

	entries := []struct {
		topic string
		body  map[string]any
	}{
		{
			topic: fmt.Sprintf("%s/sensor/%s_battery/config", cfg.DiscoveryPrefix, nodeID),
			body: map[string]any{
				"name":                "Battery",
				"unique_id":           nodeID + "_battery",
				"state_topic":         cfg.BaseTopic + "/battery",
				"availability_topic":  cfg.BaseTopic + "/status",
				"device_class":        "battery",
				"unit_of_measurement": "%",
				"device":              device,
			},
		},
		{
			topic: fmt.Sprintf("%s/binary_sensor/%s_charging/config", cfg.DiscoveryPrefix, nodeID),
			body: map[string]any{
				"name":               "Charging",
				"unique_id":          nodeID + "_charging",
				"state_topic":        cfg.BaseTopic + "/charging",
				"availability_topic": cfg.BaseTopic + "/status",
				"device_class":       "battery_charging",
				"payload_on":         "ON",
				"payload_off":        "OFF",
				"device":             device,
			},
		},
		{
			topic: fmt.Sprintf("%s/number/%s_brightness/config", cfg.DiscoveryPrefix, nodeID),
			body: map[string]any{
				"name":               "Frontlight Brightness",
				"unique_id":          nodeID + "_brightness",
				"state_topic":        cfg.BaseTopic + "/brightness",
				"command_topic":      cfg.BaseTopic + "/brightness/set",
				"availability_topic": cfg.BaseTopic + "/status",
				"min":                0,
				"max":                24,
				"step":               1,
				"device":             device,
			},
		},
	}

	for _, e := range entries {
		raw, err := json.Marshal(e.body)
		if err == nil {
			cl.Publish(e.topic, 1, true, raw)
		}
	}
}

func sanitizeID(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else if r == '-' || r == ' ' {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// ExtractValue extracts a scalar string from raw payload using an optional dot/JSONPath expression.
// Examples: "", "$.temperature", "state", "sensors[0].temp".
func ExtractValue(payload, path string) string {
	trimmed := strings.TrimSpace(payload)
	path = strings.TrimSpace(path)
	if path == "" || trimmed == "" {
		return trimmed
	}

	path = strings.TrimPrefix(path, "$.")
	path = strings.TrimPrefix(path, "$")
	path = strings.TrimPrefix(path, ".")
	if path == "" {
		return trimmed
	}

	var cur any
	if err := json.Unmarshal([]byte(trimmed), &cur); err != nil {
		return trimmed
	}

	segments := parsePathSegments(path)
	for _, seg := range segments {
		switch node := cur.(type) {
		case map[string]any:
			val, ok := node[seg.key]
			if !ok {
				return ""
			}
			cur = val
			if seg.hasIdx {
				arr, ok := cur.([]any)
				if !ok || seg.idx < 0 || seg.idx >= len(arr) {
					return ""
				}
				cur = arr[seg.idx]
			}
		case []any:
			if !seg.hasIdx || seg.idx < 0 || seg.idx >= len(node) {
				return ""
			}
			cur = node[seg.idx]
		default:
			return ""
		}
	}

	return formatJSONScalar(cur)
}

type pathSeg struct {
	key    string
	hasIdx bool
	idx    int
}

func parsePathSegments(path string) []pathSeg {
	parts := strings.Split(path, ".")
	var out []pathSeg
	for _, p := range parts {
		if p == "" {
			continue
		}
		if lb := strings.IndexByte(p, '['); lb >= 0 && strings.HasSuffix(p, "]") {
			key := p[:lb]
			idxStr := p[lb+1 : len(p)-1]
			idx, err := strconv.Atoi(idxStr)
			if err == nil {
				if key != "" {
					out = append(out, pathSeg{key: key, hasIdx: true, idx: idx})
				} else {
					out = append(out, pathSeg{hasIdx: true, idx: idx})
				}
				continue
			}
		}
		out = append(out, pathSeg{key: p})
	}
	return out
}

func formatJSONScalar(v any) string {
	switch val := v.(type) {
	case nil:
		return ""
	case string:
		return val
	case bool:
		if val {
			return "true"
		}
		return "false"
	case float64:
		if math.Abs(val-math.Round(val)) < 1e-9 {
			return strconv.FormatInt(int64(math.Round(val)), 10)
		}
		return strconv.FormatFloat(val, 'f', -1, 64)
	default:
		b, err := json.Marshal(val)
		if err != nil {
			return fmt.Sprintf("%v", val)
		}
		return string(b)
	}
}

// FormatCommand renders command_template by substituting {{value}}.
func FormatCommand(tmpl string, value float64, precision int) string {
	if tmpl == "" {
		tmpl = "{{value}}"
	}
	var formatted string
	if precision >= 0 {
		formatted = strconv.FormatFloat(value, 'f', precision, 64)
	} else {
		if math.Abs(value-math.Round(value)) < 1e-9 {
			formatted = strconv.FormatInt(int64(math.Round(value)), 10)
		} else {
			formatted = strconv.FormatFloat(value, 'f', -1, 64)
		}
	}
	return strings.ReplaceAll(tmpl, "{{value}}", formatted)
}
