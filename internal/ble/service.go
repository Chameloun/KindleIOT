package ble

import (
	"context"
	"errors"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"kindleiot/internal/config"
)

// TrackedStatus reports live visibility and signal strength for one configured device.
type TrackedStatus struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	HasIRK     bool   `json:"has_irk"`
	StaticMAC  string `json:"static_mac,omitempty"`
	CurrentMAC string `json:"current_mac,omitempty"`
	RSSI       int    `json:"rssi"`
	SeenAgoSec int    `json:"seen_ago_sec"`
	Near       bool   `json:"near"`
}

// NearbyDevice summarizes one recently observed BLE advertiser for Web UI discovery.
type NearbyDevice struct {
	MAC         string   `json:"mac"`
	AddrKind    string   `json:"addr_kind"`
	RSSI        int      `json:"rssi"`
	SeenAgoSec  int      `json:"seen_ago_sec"`
	Packets     int      `json:"packets"`
	Name        string   `json:"name,omitempty"`
	AppleTypes  []string `json:"apple_types,omitempty"`
	MatchedName string   `json:"matched_name,omitempty"`
}

// Status captures the live BLE proximity scanner state for /api/status and /api/ble/status.
type Status struct {
	Enabled       bool            `json:"enabled"`
	Running       bool            `json:"running"`
	Backend       string          `json:"backend"`
	LastError     string          `json:"last_error,omitempty"`
	Present       bool            `json:"present"`
	BestRSSI      int             `json:"best_rssi"`
	MatchedDevice string          `json:"matched_device,omitempty"`
	LastSeenSec   int             `json:"last_seen_sec"`
	PacketsTotal  uint64          `json:"packets_total"`
	Tracked       []TrackedStatus `json:"tracked"`
	Nearby        []NearbyDevice  `json:"nearby"`
}

// Callbacks notifies KindleIOT when BLE proximity changes or refreshes.
type Callbacks struct {
	OnPresenceChange func(present bool, deviceName string, rssi int)
	OnProximityPing  func(deviceName string, rssi int)
}

type compiledDevice struct {
	cfg           config.BLEDeviceConfig
	matcher       *IRKMatcher
	staticMAC     string
	lastMAC       string
	lastRSSI      int
	lastSeen      time.Time
	lastAboveAt   time.Time
	lastMatchedAt time.Time
}

type nearbyEntry struct {
	mac         string
	addrKind    string
	rssi        int
	seenAt      time.Time
	packets     int
	name        string
	appleTypes  map[string]struct{}
	matchedName string
}

// Service manages passive BLE scanning, IRK resolution, hysteresis, and away timeouts.
type Service struct {
	mu            sync.RWMutex
	cfg           config.BluetoothConfig
	isKindle      bool
	cb            Callbacks
	devices       []*compiledDevice
	nearby        map[string]*nearbyEntry
	packetsTotal  uint64
	present       bool
	bestRSSI      int
	matchedDevice string
	lastPresentAt time.Time
	lastPingAt    time.Time

	running   bool
	backend   string
	lastError string
	cancel    context.CancelFunc
	doneCh    chan struct{}
}

// NewService creates a BLE proximity scanner service.
func NewService(cfg config.BluetoothConfig, isKindle bool, cb Callbacks) *Service {
	s := &Service{
		cfg:      normalizeRuntimeCfg(cfg),
		isKindle: isKindle,
		cb:       cb,
		nearby:   make(map[string]*nearbyEntry),
	}
	s.devices = compileDevices(s.cfg.Devices)
	return s
}

func normalizeRuntimeCfg(cfg config.BluetoothConfig) config.BluetoothConfig {
	if cfg.RSSIThreshold == 0 {
		cfg.RSSIThreshold = -65
	}
	if cfg.RSSIThreshold < -100 {
		cfg.RSSIThreshold = -100
	} else if cfg.RSSIThreshold > -20 {
		cfg.RSSIThreshold = -20
	}
	if cfg.AwayTimeoutSec <= 0 {
		cfg.AwayTimeoutSec = 30
	}
	return cfg
}

func compileDevices(devs []config.BLEDeviceConfig) []*compiledDevice {
	out := make([]*compiledDevice, 0, len(devs))
	for _, d := range devs {
		cd := &compiledDevice{cfg: d}
		if d.IRK != "" {
			if m, err := NewIRKMatcher(d.IRK); err == nil {
				cd.matcher = m
			} else {
				log.Printf("ble: device %q invalid IRK ignored: %v", d.Name, err)
			}
		}
		if d.MAC != "" {
			if norm, err := config.NormalizeBLEMAC(d.MAC); err == nil {
				cd.staticMAC = norm
			} else {
				cd.staticMAC = strings.ToUpper(strings.TrimSpace(d.MAC))
			}
		}
		out = append(out, cd)
	}
	return out
}

// Start begins background BLE scanning if Bluetooth proximity is enabled.
func (s *Service) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.cfg.Enabled || s.cancel != nil {
		return
	}
	s.startLocked()
}

func (s *Service) startLocked() {
	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan struct{})
	s.cancel = cancel
	s.doneCh = doneCh
	go s.runLoop(ctx, doneCh)
}

// Stop halts background BLE scanning and cleanly restores system BT state.
func (s *Service) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	doneCh := s.doneCh
	s.cancel = nil
	s.doneCh = nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
		if doneCh != nil {
			<-doneCh
		}
	}
}

// Reconfigure updates devices/thresholds in place and starts or stops scanning when Enabled flips.
func (s *Service) Reconfigure(newCfg config.BluetoothConfig) {
	norm := normalizeRuntimeCfg(newCfg)
	newDevs := compileDevices(norm.Devices)

	s.mu.Lock()
	// Preserve live observation timestamps for devices whose ID stayed intact.
	oldByID := make(map[string]*compiledDevice, len(s.devices))
	for _, od := range s.devices {
		oldByID[od.cfg.ID] = od
	}
	for _, nd := range newDevs {
		if od, ok := oldByID[nd.cfg.ID]; ok && nd.cfg.IRK == od.cfg.IRK && nd.staticMAC == od.staticMAC {
			nd.lastMAC = od.lastMAC
			nd.lastRSSI = od.lastRSSI
			nd.lastSeen = od.lastSeen
			nd.lastAboveAt = od.lastAboveAt
			nd.lastMatchedAt = od.lastMatchedAt
		}
	}
	wasEnabled := s.cfg.Enabled
	s.cfg = norm
	s.devices = newDevs
	wasPresent := s.present
	if !norm.Enabled {
		s.present = false
		s.bestRSSI = 0
		s.matchedDevice = ""
	}
	s.mu.Unlock()

	if wasEnabled && !norm.Enabled {
		s.Stop()
		if wasPresent && s.cb.OnPresenceChange != nil {
			s.cb.OnPresenceChange(false, "", 0)
		}
	} else if !wasEnabled && norm.Enabled {
		s.Start()
	}
}

// ProcessReport ingests one decoded BLE advertising report, updates discovery state, and evaluates proximity.
func (s *Service) ProcessReport(rep AdvReport) {
	now := rep.SeenAt
	if now.IsZero() {
		now = time.Now()
	}

	var (
		presenceChanged bool
		newPresent      bool
		pingDevice      string
		pingRSSI        int
		shouldPing      bool
	)

	s.mu.Lock()
	s.packetsTotal++

	var matched *compiledDevice
	for _, d := range s.devices {
		if !d.cfg.Enabled {
			continue
		}
		if d.staticMAC != "" && strings.EqualFold(rep.MAC, d.staticMAC) {
			matched = d
			break
		}
		if d.matcher != nil && d.matcher.Matches(rep.AddrLE) {
			matched = d
			break
		}
	}

	matchedName := ""
	if matched != nil {
		matchedName = matched.cfg.Name
		matched.lastMAC = rep.MAC
		matched.lastRSSI = rep.RSSI
		matched.lastSeen = now

		thresh := s.cfg.RSSIThreshold
		if rep.RSSI >= thresh {
			recentConfirm := !matched.lastAboveAt.IsZero() && now.Sub(matched.lastAboveAt) <= 5*time.Second
			matched.lastAboveAt = now
			if rep.RSSI >= thresh+5 || s.present || recentConfirm {
				if !s.present {
					s.present = true
					presenceChanged = true
					newPresent = true
				}
				s.lastPresentAt = now
				s.matchedDevice = matched.cfg.Name
				s.bestRSSI = rep.RSSI
				matched.lastMatchedAt = now
				if s.lastPingAt.IsZero() || now.Sub(s.lastPingAt) >= 2*time.Second {
					s.lastPingAt = now
					shouldPing = true
					pingDevice = matched.cfg.Name
					pingRSSI = rep.RSSI
				}
			}
		} else if s.present && rep.RSSI >= thresh-6 {
			// Hysteresis: maintain presence until signal drops clearly below threshold - 6 dBm.
			s.lastPresentAt = now
			s.matchedDevice = matched.cfg.Name
			s.bestRSSI = rep.RSSI
			matched.lastMatchedAt = now
			if s.lastPingAt.IsZero() || now.Sub(s.lastPingAt) >= 2*time.Second {
				s.lastPingAt = now
				shouldPing = true
				pingDevice = matched.cfg.Name
				pingRSSI = rep.RSSI
			}
		}
	}

	ne, ok := s.nearby[rep.MAC]
	if !ok {
		ne = &nearbyEntry{
			mac:        rep.MAC,
			addrKind:   rep.AddrKind,
			appleTypes: make(map[string]struct{}),
		}
		s.nearby[rep.MAC] = ne
	}
	ne.rssi = rep.RSSI
	ne.seenAt = now
	ne.packets++
	if rep.Name != "" {
		ne.name = rep.Name
	}
	if matchedName != "" {
		ne.matchedName = matchedName
	}
	for _, at := range rep.AppleTypes {
		ne.appleTypes[at] = struct{}{}
	}
	// Prune stale nearby entries opportunistically so memory stays bounded.
	if len(s.nearby) > 64 {
		for k, e := range s.nearby {
			if now.Sub(e.seenAt) > 25*time.Second {
				delete(s.nearby, k)
			}
		}
	}
	s.mu.Unlock()

	if presenceChanged && s.cb.OnPresenceChange != nil {
		s.cb.OnPresenceChange(newPresent, matchedName, rep.RSSI)
	}
	if shouldPing && s.cb.OnProximityPing != nil {
		s.cb.OnProximityPing(pingDevice, pingRSSI)
	}
}

// EvaluateAway clears proximity presence if no matching packet kept presence alive within AwayTimeoutSec.
func (s *Service) EvaluateAway(now time.Time) {
	var fireAway bool

	s.mu.Lock()
	if s.present && !s.lastPresentAt.IsZero() && now.Sub(s.lastPresentAt) >= time.Duration(s.cfg.AwayTimeoutSec)*time.Second {
		s.present = false
		s.matchedDevice = ""
		s.bestRSSI = 0
		fireAway = true
	}
	for k, e := range s.nearby {
		if now.Sub(e.seenAt) > 20*time.Second {
			delete(s.nearby, k)
		}
	}
	s.mu.Unlock()

	if fireAway && s.cb.OnPresenceChange != nil {
		s.cb.OnPresenceChange(false, "", 0)
	}
}

// Status returns a snapshot of current BLE scanner health, tracked devices, and nearby advertisers.
func (s *Service) Status() Status {
	now := time.Now()
	s.mu.RLock()
	defer s.mu.RUnlock()

	st := Status{
		Enabled:       s.cfg.Enabled,
		Running:       s.running,
		Backend:       s.backend,
		LastError:     s.lastError,
		Present:       s.present,
		BestRSSI:      s.bestRSSI,
		MatchedDevice: s.matchedDevice,
		LastSeenSec:   -1,
		PacketsTotal:  s.packetsTotal,
		Tracked:       make([]TrackedStatus, 0, len(s.devices)),
		Nearby:        make([]NearbyDevice, 0, len(s.nearby)),
	}
	if !s.lastPresentAt.IsZero() {
		st.LastSeenSec = int(now.Sub(s.lastPresentAt).Seconds())
		if st.LastSeenSec < 0 {
			st.LastSeenSec = 0
		}
	}

	awayDur := time.Duration(s.cfg.AwayTimeoutSec) * time.Second
	for _, d := range s.devices {
		seenAgo := -1
		if !d.lastSeen.IsZero() {
			seenAgo = int(now.Sub(d.lastSeen).Seconds())
			if seenAgo < 0 {
				seenAgo = 0
			}
		}
		near := d.cfg.Enabled && !d.lastMatchedAt.IsZero() && now.Sub(d.lastMatchedAt) < awayDur && s.present
		st.Tracked = append(st.Tracked, TrackedStatus{
			ID:         d.cfg.ID,
			Name:       d.cfg.Name,
			Enabled:    d.cfg.Enabled,
			HasIRK:     d.matcher != nil,
			StaticMAC:  d.staticMAC,
			CurrentMAC: d.lastMAC,
			RSSI:       d.lastRSSI,
			SeenAgoSec: seenAgo,
			Near:       near,
		})
	}

	for _, e := range s.nearby {
		if now.Sub(e.seenAt) > 20*time.Second {
			continue
		}
		ago := int(now.Sub(e.seenAt).Seconds())
		if ago < 0 {
			ago = 0
		}
		ap := make([]string, 0, len(e.appleTypes))
		for k := range e.appleTypes {
			ap = append(ap, k)
		}
		sort.Strings(ap)
		st.Nearby = append(st.Nearby, NearbyDevice{
			MAC:         e.mac,
			AddrKind:    e.addrKind,
			RSSI:        e.rssi,
			SeenAgoSec:  ago,
			Packets:     e.packets,
			Name:        e.name,
			AppleTypes:  ap,
			MatchedName: e.matchedName,
		})
	}
	sort.Slice(st.Nearby, func(i, j int) bool {
		if st.Nearby[i].RSSI != st.Nearby[j].RSSI {
			return st.Nearby[i].RSSI > st.Nearby[j].RSSI
		}
		return st.Nearby[i].Packets > st.Nearby[j].Packets
	})
	if len(st.Nearby) > 30 {
		st.Nearby = st.Nearby[:30]
	}
	return st
}

func (s *Service) runLoop(ctx context.Context, doneCh chan struct{}) {
	defer close(doneCh)

	if !s.isKindle {
		s.mu.Lock()
		s.backend = "desktop"
		s.lastError = "BLE hardware scanner runs only on Kindle"
		s.running = false
		s.mu.Unlock()
		<-ctx.Done()
		return
	}

	awayTicker := time.NewTicker(1 * time.Second)
	defer awayTicker.Stop()

	for ctx.Err() == nil {
		tr, err := openHardwareTransport(ctx)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			s.mu.Lock()
			s.running = false
			s.lastError = err.Error()
			s.mu.Unlock()
			log.Printf("ble: hardware open error: %v", err)

			timer := time.NewTimer(5 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}

		s.mu.Lock()
		s.running = true
		s.backend = tr.BackendName()
		s.lastError = ""
		s.mu.Unlock()
		log.Printf("ble: scanner started on backend %s", tr.BackendName())

		lastEventAt := time.Now()
		for ctx.Err() == nil {
			select {
			case <-ctx.Done():
				goto shutdownTransport
			case now := <-awayTicker.C:
				s.EvaluateAway(now)
			default:
			}

			evtCode, payload, rerr := tr.ReadEvent(500 * time.Millisecond)
			now := time.Now()
			select {
			case <-awayTicker.C:
				s.EvaluateAway(now)
			default:
			}

			if rerr != nil {
				if errors.Is(rerr, errReadTimeout) {
					if now.Sub(lastEventAt) > 45*time.Second {
						log.Printf("ble: watchdog >45s without HCI events; restarting transport")
						s.mu.Lock()
						s.lastError = "watchdog timeout (>45s without HCI events)"
						s.mu.Unlock()
						break
					}
					continue
				}
				if ctx.Err() == nil {
					log.Printf("ble: read error (%v); restarting transport", rerr)
					s.mu.Lock()
					s.lastError = rerr.Error()
					s.mu.Unlock()
				}
				break
			}

			lastEventAt = now
			if evtCode == 0x3E && len(payload) >= 2 && payload[0] == 0x02 {
				for _, rep := range ParseAdvertisingReport(payload, now) {
					s.ProcessReport(rep)
				}
			}
		}

	shutdownTransport:
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		_ = tr.Close()

		if ctx.Err() != nil {
			break
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}

	s.mu.Lock()
	s.running = false
	s.mu.Unlock()
}
