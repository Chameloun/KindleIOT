//go:build linux

package ble

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	crtscts   = uint32(0x80000000)
	cbaudMask = uint32(0x100F)
	tcflsh    = uintptr(0x540B)
	tcioflush = uintptr(2)

	brcmUARTPath     = "/dev/ttymxc2"
	mtkDevPath       = "/dev/stpbt"
	btSleepDir       = "/proc/bluetooth/sleep"
	btSleepProto     = "/proc/bluetooth/sleep/proto"
	btDevWake        = "/proc/bluetooth/sleep/btwake"
	btProcEnable     = "/proc/bluetooth/btenable"
	cpuDmaLatencyDev = "/dev/cpu_dma_latency"
)

func openHardwareTransport(ctx context.Context) (hciTransport, error) {
	if fileExists(brcmUARTPath) && fileExists(btProcEnable) {
		return openBroadcomUART(ctx)
	}
	if fileExists(mtkDevPath) {
		return openMediaTekHCI(ctx)
	}
	return nil, errHardwareUnavailable
}

type h4Conn struct {
	fd int
}

func (c *h4Conn) drain(d time.Duration) {
	deadline := time.Now().Add(d)
	buf := make([]byte, 256)
	for time.Now().Before(deadline) {
		n, err := syscall.Read(c.fd, buf)
		if n > 0 {
			continue
		}
		if err != nil && !errors.Is(err, syscall.EINTR) && !errors.Is(err, syscall.EAGAIN) {
			break
		}
	}
}

func (c *h4Conn) readExact(buf []byte, deadline time.Time) error {
	off := 0
	for off < len(buf) {
		if time.Now().After(deadline) {
			return errReadTimeout
		}
		n, err := syscall.Read(c.fd, buf[off:])
		if n > 0 {
			off += n
			continue
		}
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			if errors.Is(err, syscall.EAGAIN) {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			return err
		}
	}
	return nil
}

func (c *h4Conn) ReadEvent(timeout time.Duration) (byte, []byte, error) {
	deadline := time.Now().Add(timeout)
	var single [1]byte
	for {
		if err := c.readExact(single[:], deadline); err != nil {
			return 0, nil, err
		}
		if single[0] == 0x04 {
			break
		}
	}
	frameDeadline := deadline
	if time.Until(frameDeadline) < 300*time.Millisecond {
		frameDeadline = time.Now().Add(300 * time.Millisecond)
	}
	var hdr [2]byte
	if err := c.readExact(hdr[:], frameDeadline); err != nil {
		return 0, nil, fmt.Errorf("read hci header: %w", err)
	}
	plen := int(hdr[1])
	payload := make([]byte, plen)
	if plen > 0 {
		if err := c.readExact(payload, frameDeadline); err != nil {
			return 0, nil, fmt.Errorf("read hci payload (%d): %w", plen, err)
		}
	}
	return hdr[0], payload, nil
}

func (c *h4Conn) writeAll(data []byte) error {
	off := 0
	for off < len(data) {
		n, err := syscall.Write(c.fd, data[off:])
		if n > 0 {
			off += n
			continue
		}
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			if errors.Is(err, syscall.EAGAIN) {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			return err
		}
		return io.ErrShortWrite
	}
	return nil
}

func (c *h4Conn) sendCmd(opcode uint16, params []byte, timeout time.Duration) (byte, []byte, error) {
	if len(params) > 255 {
		return 0, nil, fmt.Errorf("hci params too long (%d)", len(params))
	}
	pkt := make([]byte, 4+len(params))
	pkt[0] = 0x01
	binary.LittleEndian.PutUint16(pkt[1:3], opcode)
	pkt[3] = byte(len(params))
	copy(pkt[4:], params)
	if err := c.writeAll(pkt); err != nil {
		return 0, nil, fmt.Errorf("write hci 0x%04X: %w", opcode, err)
	}

	deadline := time.Now().Add(timeout)
	for {
		rem := time.Until(deadline)
		if rem <= 0 {
			return 0, nil, fmt.Errorf("hci 0x%04X: %w", opcode, errReadTimeout)
		}
		evtCode, payload, err := c.ReadEvent(rem)
		if err != nil {
			return 0, nil, fmt.Errorf("wait hci 0x%04X: %w", opcode, err)
		}
		if evtCode == 0x0E && len(payload) >= 4 {
			if binary.LittleEndian.Uint16(payload[1:3]) == opcode {
				return payload[3], payload[4:], nil
			}
		} else if evtCode == 0x0F && len(payload) >= 4 {
			if binary.LittleEndian.Uint16(payload[2:4]) == opcode && payload[0] != 0x00 {
				return payload[0], nil, fmt.Errorf("hci cmd status 0x%02X for 0x%04X", payload[0], opcode)
			}
		}
	}
}

func (c *h4Conn) enablePassiveScan() error {
	// Wi-Fi friendly 20% passive duty cycle: 200ms interval (0x0140), 40ms window (0x0040).
	cmds := []struct {
		name   string
		opcode uint16
		params []byte
	}{
		{"HCI_Set_Event_Mask", 0x0C01, []byte{0xFF, 0xFF, 0xFB, 0xFF, 0x07, 0xF8, 0xBF, 0x3D}},
		{"HCI_LE_Set_Event_Mask", 0x2001, []byte{0x1F, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}},
		{"HCI_LE_Set_Scan_Parameters", 0x200B, []byte{0x00, 0x40, 0x01, 0x40, 0x00, 0x00, 0x00}},
		{"HCI_LE_Set_Scan_Enable(ON)", 0x200C, []byte{0x01, 0x00}},
	}
	for _, cmd := range cmds {
		st, _, err := c.sendCmd(cmd.opcode, cmd.params, 1500*time.Millisecond)
		if err != nil || st != 0x00 {
			return fmt.Errorf("%s failed (status=0x%02X err=%v)", cmd.name, st, err)
		}
	}
	return nil
}

func (c *h4Conn) disablePassiveScan() {
	if c.fd >= 0 {
		_, _, _ = c.sendCmd(0x200C, []byte{0x00, 0x00}, 500*time.Millisecond)
	}
}

type cpuidleSaved struct {
	path  string
	value string
}

type latencyHold struct {
	fd    *os.File
	saved []cpuidleSaved
}

type brcmTransport struct {
	conn      *h4Conn
	hold      latencyHold
	closeOnce sync.Once
}

func (t *brcmTransport) BackendName() string {
	return "broadcom-uart"
}

func (t *brcmTransport) ReadEvent(timeout time.Duration) (byte, []byte, error) {
	return t.conn.ReadEvent(timeout)
}

func (t *brcmTransport) Close() error {
	t.closeOnce.Do(func() {
		if t.conn != nil {
			t.conn.disablePassiveScan()
			_ = syscall.Close(t.conn.fd)
			t.conn.fd = -1
		}
		releaseCPULatency(&t.hold)
		thawBtd()
		if raw, err := os.ReadFile(btProcEnable); err == nil && strings.TrimSpace(string(raw)) != "0" {
			_ = os.WriteFile(btProcEnable, []byte("0"), 0644)
		}
		_, _ = runCmdTimeout(5*time.Second, "initctl", "restart", "btd")
	})
	return nil
}

func openBroadcomUART(ctx context.Context) (*brcmTransport, error) {
	if !warmUpChip(ctx) {
		return nil, fmt.Errorf("could not warm BCM4343 chip via bsa_server")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	handoffFromBSA()
	tr := &brcmTransport{}
	keep := false
	defer func() {
		if !keep {
			_ = tr.Close()
		}
	}()

	assertWake()
	tr.hold = acquireCPULatency()
	if !sleepContext(ctx, 500*time.Millisecond) {
		return nil, ctx.Err()
	}

	fd, err := syscall.Open(brcmUARTPath, syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", brcmUARTPath, err)
	}
	if err := syscall.SetNonblock(fd, false); err != nil {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("clear nonblock on %s: %w", brcmUARTPath, err)
	}
	if err := configureUART(fd, syscall.B2000000, true); err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}

	tr.conn = &h4Conn{fd: fd}
	tr.conn.drain(150 * time.Millisecond)

	var resetOK bool
	for attempt := 1; attempt <= 5; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		st, _, rerr := tr.conn.sendCmd(0x0C03, nil, 1*time.Second)
		if rerr == nil && st == 0x00 {
			resetOK = true
			break
		}
		wakePulse()
		if !sleepContext(ctx, 200*time.Millisecond) {
			return nil, ctx.Err()
		}
		tr.conn.drain(100 * time.Millisecond)
	}
	if !resetOK {
		return nil, fmt.Errorf("BCM4343 never answered HCI_Reset")
	}

	if err := tr.conn.enablePassiveScan(); err != nil {
		return nil, err
	}
	keep = true
	return tr, nil
}

type mtkTransport struct {
	conn      *h4Conn
	closeOnce sync.Once
}

func (t *mtkTransport) BackendName() string {
	return "mediatek-stpbt"
}

func (t *mtkTransport) ReadEvent(timeout time.Duration) (byte, []byte, error) {
	return t.conn.ReadEvent(timeout)
}

func (t *mtkTransport) Close() error {
	t.closeOnce.Do(func() {
		if t.conn != nil {
			t.conn.disablePassiveScan()
			_ = syscall.Close(t.conn.fd)
			t.conn.fd = -1
		}
	})
	return nil
}

func openMediaTekHCI(ctx context.Context) (*mtkTransport, error) {
	fd, err := syscall.Open(mtkDevPath, syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", mtkDevPath, err)
	}
	_ = syscall.SetNonblock(fd, false)
	tr := &mtkTransport{conn: &h4Conn{fd: fd}}
	keep := false
	defer func() {
		if !keep {
			_ = tr.Close()
		}
	}()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if st, _, rerr := tr.conn.sendCmd(0x0C03, nil, 1500*time.Millisecond); rerr != nil || st != 0x00 {
		return nil, fmt.Errorf("stpbt HCI_Reset failed (status=0x%02X err=%v)", st, rerr)
	}
	if err := tr.conn.enablePassiveScan(); err != nil {
		return nil, err
	}
	keep = true
	return tr, nil
}

func findPIDsByName(name string) []int {
	comms, _ := filepath.Glob("/proc/[0-9]*/comm")
	var pids []int
	for _, cpath := range comms {
		raw, err := os.ReadFile(cpath)
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(raw)) == name {
			if pid, err := strconv.Atoi(filepath.Base(filepath.Dir(cpath))); err == nil {
				pids = append(pids, pid)
			}
		}
	}
	sort.Ints(pids)
	return pids
}

func thawBtd() {
	for _, pid := range findPIDsByName("btd") {
		_ = syscall.Kill(pid, syscall.SIGCONT)
	}
}

func waitForBSA(ctx context.Context, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return false
		}
		if len(findPIDsByName("bsa_server")) > 0 {
			return sleepContext(ctx, 3*time.Second)
		}
		if !sleepContext(ctx, 500*time.Millisecond) {
			return false
		}
	}
	return false
}

func warmUpChip(ctx context.Context) bool {
	if len(findPIDsByName("bsa_server")) > 0 {
		return sleepContext(ctx, 3*time.Second)
	}
	thawBtd()
	_, _ = runCmdTimeout(5*time.Second, "lipc-set-prop", "-s", "com.lab126.btfd", "BTenable", "1:1")
	if waitForBSA(ctx, 12*time.Second) {
		return true
	}
	if ctx.Err() != nil {
		return false
	}
	_, _ = runCmdTimeout(5*time.Second, "initctl", "restart", "btd")
	if !sleepContext(ctx, 2*time.Second) {
		return false
	}
	_, _ = runCmdTimeout(5*time.Second, "lipc-set-prop", "-s", "com.lab126.btfd", "BTenable", "1:1")
	return waitForBSA(ctx, 12*time.Second)
}

func handoffFromBSA() {
	for _, pid := range findPIDsByName("btd") {
		_ = syscall.Kill(pid, syscall.SIGSTOP)
	}
	for _, pid := range findPIDsByName("bsa_server") {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	time.Sleep(500 * time.Millisecond)

	me := os.Getpid()
	fdPaths, _ := filepath.Glob("/proc/[0-9]*/fd/*")
	for _, fp := range fdPaths {
		target, err := os.Readlink(fp)
		if err != nil || target != brcmUARTPath {
			continue
		}
		parts := strings.Split(fp, "/")
		if len(parts) < 4 {
			continue
		}
		if pid, perr := strconv.Atoi(parts[2]); perr == nil && pid != me {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	time.Sleep(300 * time.Millisecond)
}

func assertWake() {
	_ = os.WriteFile(btSleepProto, []byte("0"), 0644)
	_ = os.WriteFile(btDevWake, []byte("0"), 0644)
}

func wakePulse() {
	_ = os.WriteFile(btDevWake, []byte("1"), 0644)
	time.Sleep(50 * time.Millisecond)
	_ = os.WriteFile(btDevWake, []byte("0"), 0644)
}

func acquireCPULatency() latencyHold {
	var hold latencyHold
	f, err := os.OpenFile(cpuDmaLatencyDev, os.O_WRONLY, 0)
	if err == nil {
		buf := make([]byte, 4)
		binary.LittleEndian.PutUint32(buf, 50)
		if _, werr := f.Write(buf); werr == nil {
			hold.fd = f
			return hold
		}
		_ = f.Close()
	}

	dirs, _ := filepath.Glob("/sys/devices/system/cpu/cpu*/cpuidle")
	sort.Strings(dirs)
	for _, d := range dirs {
		states, _ := filepath.Glob(filepath.Join(d, "state*"))
		sort.Strings(states)
		for _, stDir := range states {
			latRaw, lerr := os.ReadFile(filepath.Join(stDir, "latency"))
			if lerr != nil {
				continue
			}
			lat, perr := strconv.Atoi(strings.TrimSpace(string(latRaw)))
			if perr != nil || lat <= 80 {
				continue
			}
			disPath := filepath.Join(stDir, "disable")
			origRaw, oerr := os.ReadFile(disPath)
			if oerr != nil {
				continue
			}
			orig := strings.TrimSpace(string(origRaw))
			if orig == "1" {
				continue
			}
			if err := os.WriteFile(disPath, []byte("1"), 0644); err == nil {
				hold.saved = append(hold.saved, cpuidleSaved{path: disPath, value: orig})
			}
		}
	}
	return hold
}

func releaseCPULatency(hold *latencyHold) {
	if hold.fd != nil {
		_ = hold.fd.Close()
		hold.fd = nil
	}
	for _, item := range hold.saved {
		_ = os.WriteFile(item.path, []byte(item.value), 0644)
	}
	hold.saved = nil
}

func configureUART(fd int, baud uint32, flowControl bool) error {
	var t syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&t))); errno != 0 {
		return fmt.Errorf("TCGETS: %w", errno)
	}
	t.Iflag = 0
	t.Oflag = 0
	t.Lflag = 0
	t.Cflag = (t.Cflag &^ (cbaudMask | syscall.CSIZE | syscall.PARENB | syscall.CSTOPB | crtscts)) |
		baud | syscall.CS8 | syscall.CREAD | syscall.CLOCAL
	if flowControl {
		t.Cflag |= crtscts
	}
	t.Ispeed = baud
	t.Ospeed = baud
	t.Cc[syscall.VMIN] = 0
	t.Cc[syscall.VTIME] = 1
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(syscall.TCSETS), uintptr(unsafe.Pointer(&t))); errno != 0 {
		return fmt.Errorf("TCSETS: %w", errno)
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), tcflsh, tcioflush); errno != 0 {
		return fmt.Errorf("TCFLSH: %w", errno)
	}
	return nil
}

func runCmdTimeout(timeout time.Duration, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return strings.TrimSpace(out.String()), err
	case <-timer.C:
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
		log.Printf("ble: command %s %v timed out", name, args)
		return strings.TrimSpace(out.String()), errReadTimeout
	}
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
