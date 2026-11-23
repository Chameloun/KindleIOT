package kindle

import (
	"context"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Status describes current Kindle hardware telemetry.
type Status struct {
	IsKindle   bool
	Battery    int
	Charging   bool
	Brightness int
	IP         string
}

// System wraps Kindle LIPC and sysfs hardware interactions with desktop fallbacks.
type System struct {
	mu            sync.Mutex
	isKindle      bool
	preventSleep  bool
	simBrightness int
}

// NewSystem detects whether the process is running on a Kindle device.
func NewSystem() *System {
	_, err := exec.LookPath("lipc-get-prop")
	isKindle := err == nil && fileExists("/etc/prettyversion.txt")
	return &System{
		isKindle:      isKindle,
		simBrightness: 12,
	}
}

// IsKindle reports true when running on real Kindle hardware.
func (s *System) IsKindle() bool {
	return s.isKindle
}

// SetPreventScreenSaver enables or disables the Kindle sleep/screensaver inhibitor.
func (s *System) SetPreventScreenSaver(enable bool) {
	s.mu.Lock()
	s.preventSleep = enable
	s.mu.Unlock()
	if !s.isKindle {
		return
	}
	val := "0"
	if enable {
		val = "1"
	}
	_ = exec.Command("lipc-set-prop", "-i", "com.lab126.powerd", "preventScreenSaver", val).Run()
}

// SetBrightness adjusts frontlight intensity (0..24 on KT4).
func (s *System) SetBrightness(level int) int {
	if level < 0 {
		level = 0
	}
	if level > 24 {
		level = 24
	}
	s.mu.Lock()
	s.simBrightness = level
	s.mu.Unlock()
	if s.isKindle {
		_ = exec.Command("lipc-set-prop", "-i", "com.lab126.powerd", "flIntensity", strconv.Itoa(level)).Run()
	}
	return level
}

// GetBrightness returns the current frontlight level (0..24), or -1 if unavailable.
func (s *System) GetBrightness() int {
	if !s.isKindle {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.simBrightness
	}
	out, err := exec.Command("lipc-get-prop", "com.lab126.powerd", "flIntensity").Output()
	if err != nil {
		return -1
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return -1
	}
	return v
}

// ReadStatus collects battery percentage, charging status, brightness, and local IP.
func (s *System) ReadStatus() Status {
	st := Status{
		IsKindle:   s.isKindle,
		Battery:    -1,
		Brightness: s.GetBrightness(),
		IP:         localIPv4(),
	}
	if !s.isKindle {
		st.Battery = 100
		st.Charging = true
		return st
	}

	if out, err := exec.Command("lipc-get-prop", "com.lab126.powerd", "battLevel").Output(); err == nil {
		if v, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil {
			st.Battery = v
		}
	}
	if out, err := exec.Command("lipc-get-prop", "com.lab126.powerd", "isCharging").Output(); err == nil {
		st.Charging = strings.TrimSpace(string(out)) == "1"
	}
	return st
}

// AllowTCPPort opens an incoming TCP port in Kindle's iptables INPUT chain if not already allowed.
func (s *System) AllowTCPPort(port string) {
	s.allowPort("tcp", port)
}

// AllowUDPPort opens an incoming UDP port (such as mDNS 5353) in Kindle's iptables INPUT chain.
func (s *System) AllowUDPPort(port string) {
	s.allowPort("udp", port)
}

// KeepWiFiAwake disables aggressive Wi-Fi power saving on Kindle so HomeKit mDNS and TCP event
// connections from Apple Home hubs are never dropped or delayed when the screen is idle.
func (s *System) KeepWiFiAwake() {
	if !s.isKindle {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := exec.LookPath("iwconfig"); err == nil {
		_ = exec.CommandContext(ctx, "iwconfig", "wlan0", "power", "off").Run()
	}
	if _, err := exec.LookPath("iw"); err == nil {
		_ = exec.CommandContext(ctx, "iw", "dev", "wlan0", "set", "power_save", "off").Run()
	}
}

func (s *System) allowPort(proto, port string) {
	if !s.isKindle || port == "" {
		return
	}
	if _, err := exec.LookPath("iptables"); err != nil {
		return
	}
	if err := exec.Command("iptables", "-C", "INPUT", "-p", proto, "--dport", port, "-j", "ACCEPT").Run(); err != nil {
		_ = exec.Command("iptables", "-I", "INPUT", "1", "-p", proto, "--dport", port, "-j", "ACCEPT").Run()
	}
}

func localIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
				if v4 := ipnet.IP.To4(); v4 != nil {
					return v4.String()
				}
			}
		}
	}
	return ""
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
