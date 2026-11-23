package mqtt

import (
	"fmt"
	"sync"

	"kindleiot/internal/config"
)

// BrokerStatus summarizes connection state and cached topics for one MQTT instance.
type BrokerStatus struct {
	ID        string                `json:"id"`
	Name      string                `json:"name"`
	Broker    string                `json:"broker"`
	Connected bool                  `json:"connected"`
	LastError string                `json:"last_error"`
	Topics    map[string]TopicValue `json:"topics"`
}

// ManagerSnapshot captures status across all configured MQTT instances.
type ManagerSnapshot struct {
	AllConnected   bool                    `json:"all_connected"`
	AnyConnected   bool                    `json:"any_connected"`
	ConnectedCount int                     `json:"connected_count"`
	TotalCount     int                     `json:"total_count"`
	FirstError     string                  `json:"first_error"`
	Brokers        map[string]BrokerStatus `json:"brokers"`
}

// Manager orchestrates one or more named MQTT Client instances and routes per-widget calls.
type Manager struct {
	mu         sync.RWMutex
	primaryID  string
	order      []string
	clients    map[string]*Client
	configs    map[string]config.MQTTConfig
	onUpdate   func()
	cmdHandler CommandHandler
}

// NewManager creates an MQTT Manager from the full configuration.
func NewManager(cfg *config.Config, onUpdate func(), cmd CommandHandler) *Manager {
	m := &Manager{
		primaryID:  cfg.PrimaryBrokerID(),
		clients:    make(map[string]*Client),
		configs:    make(map[string]config.MQTTConfig),
		onUpdate:   onUpdate,
		cmdHandler: cmd,
	}
	topicsByBroker := cfg.SubscribeTopicsByBroker()
	for _, b := range cfg.EffectiveBrokers() {
		m.order = append(m.order, b.ID)
		m.configs[b.ID] = b
		m.clients[b.ID] = NewClient(b, topicsByBroker[b.ID], onUpdate, cmd)
	}
	return m
}

// Connect starts all configured MQTT broker connections.
func (m *Manager) Connect() {
	m.mu.RLock()
	list := make([]*Client, 0, len(m.order))
	for _, id := range m.order {
		if cl := m.clients[id]; cl != nil {
			list = append(list, cl)
		}
	}
	m.mu.RUnlock()

	for _, cl := range list {
		_ = cl.Connect()
	}
}

// Disconnect closes all MQTT connections cleanly.
func (m *Manager) Disconnect() {
	m.mu.RLock()
	list := make([]*Client, 0, len(m.clients))
	for _, cl := range m.clients {
		list = append(list, cl)
	}
	m.mu.RUnlock()

	for _, cl := range list {
		cl.Disconnect()
	}
}

// Reconfigure applies updated broker definitions and per-broker topic subscriptions.
// Unchanged brokers keep their existing TCP/TLS connection and only update subscriptions.
func (m *Manager) Reconfigure(cfg *config.Config) {
	topicsByBroker := cfg.SubscribeTopicsByBroker()
	brokers := cfg.EffectiveBrokers()

	m.mu.Lock()
	m.primaryID = cfg.PrimaryBrokerID()
	newSet := make(map[string]config.MQTTConfig, len(brokers))
	newOrder := make([]string, 0, len(brokers))
	for _, b := range brokers {
		newSet[b.ID] = b
		newOrder = append(newOrder, b.ID)
	}

	var toDisconnect []*Client
	for id, cl := range m.clients {
		if _, exists := newSet[id]; !exists {
			toDisconnect = append(toDisconnect, cl)
			delete(m.clients, id)
			delete(m.configs, id)
		}
	}

	type reconfAction struct {
		client *Client
		cfg    config.MQTTConfig
		topics []string
		isNew  bool
	}
	var actions []reconfAction
	for _, b := range brokers {
		if cl, exists := m.clients[b.ID]; exists {
			m.configs[b.ID] = b
			actions = append(actions, reconfAction{client: cl, cfg: b, topics: topicsByBroker[b.ID]})
		} else {
			cl := NewClient(b, topicsByBroker[b.ID], m.onUpdate, m.cmdHandler)
			m.clients[b.ID] = cl
			m.configs[b.ID] = b
			actions = append(actions, reconfAction{client: cl, cfg: b, topics: topicsByBroker[b.ID], isNew: true})
		}
	}
	m.order = newOrder
	m.mu.Unlock()

	for _, cl := range toDisconnect {
		cl.Disconnect()
	}
	for _, act := range actions {
		if act.isNew {
			_ = act.client.Connect()
		} else {
			act.client.Reconfigure(act.cfg, act.topics)
		}
	}
}

func (m *Manager) resolveClient(brokerID string) (*Client, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if brokerID == "" {
		brokerID = m.primaryID
	}
	return m.clients[brokerID], brokerID
}

// GetTopic returns the cached payload for topic on brokerID (defaults to primary broker).
func (m *Manager) GetTopic(brokerID, topic string) (TopicValue, bool) {
	cl, _ := m.resolveClient(brokerID)
	if cl == nil {
		return TopicValue{}, false
	}
	return cl.GetTopic(topic)
}

// SetLocalValue records an optimistic/local payload on brokerID.
func (m *Manager) SetLocalValue(brokerID, topic, payload string) {
	cl, _ := m.resolveClient(brokerID)
	if cl != nil {
		cl.SetLocalValue(topic, payload)
	}
}

// GetHistory returns recent numeric samples for topic on brokerID.
func (m *Manager) GetHistory(brokerID, topic, jsonPath string, maxPoints int) []HistoryPoint {
	cl, _ := m.resolveClient(brokerID)
	if cl == nil {
		return nil
	}
	return cl.GetHistory(topic, jsonPath, maxPoints)
}

// Publish sends an MQTT message to the specified brokerID.
func (m *Manager) Publish(brokerID, topic string, qos byte, retain bool, payload string) error {
	cl, resolved := m.resolveClient(brokerID)
	if cl == nil {
		return fmt.Errorf("unknown mqtt broker %q", resolved)
	}
	return cl.Publish(topic, qos, retain, payload)
}

// PublishTelemetry publishes Kindle telemetry to the primary broker.
func (m *Manager) PublishTelemetry(battery int, charging bool, ip string, brightness int) {
	cl, _ := m.resolveClient("")
	if cl != nil {
		cl.PublishTelemetry(battery, charging, ip, brightness)
	}
}

// Snapshot returns per-broker status and combined summary across all MQTT instances.
func (m *Manager) Snapshot() ManagerSnapshot {
	m.mu.RLock()
	ids := append([]string(nil), m.order...)
	cfgs := make(map[string]config.MQTTConfig, len(m.configs))
	clients := make(map[string]*Client, len(m.clients))
	for _, id := range ids {
		cfgs[id] = m.configs[id]
		clients[id] = m.clients[id]
	}
	m.mu.RUnlock()

	snap := ManagerSnapshot{
		TotalCount: len(ids),
		Brokers:    make(map[string]BrokerStatus, len(ids)),
	}
	for _, id := range ids {
		cl := clients[id]
		bCfg := cfgs[id]
		conn, lastErr, vals := cl.Snapshot()
		if conn {
			snap.ConnectedCount++
			snap.AnyConnected = true
		} else if snap.FirstError == "" && lastErr != "" {
			snap.FirstError = id + ": " + lastErr
		}
		snap.Brokers[id] = BrokerStatus{
			ID:        id,
			Name:      bCfg.Name,
			Broker:    bCfg.Broker,
			Connected: conn,
			LastError: lastErr,
			Topics:    vals,
		}
	}
	snap.AllConnected = snap.TotalCount > 0 && snap.ConnectedCount == snap.TotalCount
	return snap
}
