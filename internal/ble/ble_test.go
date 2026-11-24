package ble

import (
	"encoding/base64"
	"encoding/hex"
	"testing"
	"time"

	"kindleiot/internal/config"
)

func TestDecodeIRKAndRPAMatch(t *testing.T) {
	// Bluetooth Core Spec Vol 3 Part H D.7: IRK = ec0234a357c8ad05341010a60a397d9b,
	// prand = 708194, ah(IRK, 708194) = 0dfbaa -> RPA 70:81:94:0D:FB:AA.
	irkHex := "ec0234a357c8ad05341010a60a397d9b"
	irkBytes, err := hex.DecodeString(irkHex)
	if err != nil {
		t.Fatalf("decode vector hex: %v", err)
	}
	b64 := base64.StdEncoding.EncodeToString(irkBytes)

	revBytes := make([]byte, 16)
	for i := 0; i < 16; i++ {
		revBytes[i] = irkBytes[15-i]
	}
	revHex := hex.EncodeToString(revBytes)

	addrLE := [6]byte{0xAA, 0xFB, 0x0D, 0x94, 0x81, 0x70}
	if FormatMACLE(addrLE) != "70:81:94:0D:FB:AA" {
		t.Fatalf("unexpected formatted MAC: %s", FormatMACLE(addrLE))
	}

	var rawIRK [16]byte
	copy(rawIRK[:], irkBytes)
	if !ResolveRPA(addrLE, rawIRK) {
		t.Fatalf("ResolveRPA failed on Core Spec test vector")
	}

	for _, variant := range []string{irkHex, "irk:" + irkHex, b64, revHex} {
		m, err := NewIRKMatcher(variant)
		if err != nil {
			t.Fatalf("NewIRKMatcher(%q) failed: %v", variant, err)
		}
		if !m.Matches(addrLE) {
			t.Fatalf("IRKMatcher(%q) did not match RPA %s", variant, FormatMACLE(addrLE))
		}
	}

	m, err := NewIRKMatcher(b64)
	if err != nil {
		t.Fatalf("NewIRKMatcher failed: %v", err)
	}
	// Static random address (MSBs = 0b11) must be rejected.
	nonRPA := [6]byte{0xAA, 0xFB, 0x0D, 0x94, 0x81, 0xF0}
	if m.Matches(nonRPA) {
		t.Fatalf("expected non-RPA address %s to be rejected", FormatMACLE(nonRPA))
	}

	// Wrong IRK must be rejected.
	wrongM, err := NewIRKMatcher("00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("NewIRKMatcher wrong failed: %v", err)
	}
	if wrongM.Matches(addrLE) {
		t.Fatalf("expected mismatched IRK not to resolve RPA")
	}
}

func TestParseAdvertisingReport(t *testing.T) {
	now := time.Now()

	// N=1: Apple NearbyInfo report
	ad1 := []byte{
		0x02, 0x01, 0x1A,
		0x0A, 0xFF, 0x4C, 0x00, 0x10, 0x05, 0x01, 0x02, 0x03, 0x04, 0x05,
	}
	pkt1 := []byte{0x02, 0x01, 0x00, 0x01, 0xAA, 0xFB, 0x0D, 0x94, 0x81, 0x70, byte(len(ad1))}
	r1 := int8(-48)
	pkt1 = append(pkt1, ad1...)
	pkt1 = append(pkt1, byte(r1))

	reps1 := ParseAdvertisingReport(pkt1, now)
	if len(reps1) != 1 {
		t.Fatalf("expected 1 report, got %d", len(reps1))
	}
	if reps1[0].MAC != "70:81:94:0D:FB:AA" || reps1[0].AddrKind != "Random-RPA" || reps1[0].RSSI != -48 {
		t.Fatalf("unexpected report 1 metadata: %+v", reps1[0])
	}
	if len(reps1[0].AppleTypes) != 1 || reps1[0].AppleTypes[0] != "NearbyInfo" {
		t.Fatalf("expected NearbyInfo Apple type, got %v", reps1[0].AppleTypes)
	}

	// N=2 columnar report: Device A (AirPlay + Name "Living") + Device B (FindMy)
	adA := []byte{
		0x07, 0x09, 'L', 'i', 'v', 'i', 'n', 'g',
		0x07, 0xFF, 0x4C, 0x00, 0x09, 0x02, 0x11, 0x22,
	}
	adB := []byte{
		0x06, 0xFF, 0x4C, 0x00, 0x12, 0x01, 0xAA,
	}
	pkt2 := []byte{
		0x02, 0x02, // subevent 0x02, num_reports=2
		0x00, 0x03, // Event_Type[2]
		0x00, 0x01, // Address_Type[2]
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, // Addr 0 (Public)
		0x11, 0x22, 0x33, 0x44, 0x55, 0xC6, // Addr 1 (Random-St)
		byte(len(adA)), byte(len(adB)), // Length_Data[2]
	}
	rA, rB := int8(-35), int8(-72)
	pkt2 = append(pkt2, adA...)
	pkt2 = append(pkt2, adB...)
	pkt2 = append(pkt2, byte(rA), byte(rB)) // RSSI[2]

	reps2 := ParseAdvertisingReport(pkt2, now)
	if len(reps2) != 2 {
		t.Fatalf("expected 2 columnar reports, got %d", len(reps2))
	}
	if reps2[0].MAC != "06:05:04:03:02:01" || reps2[0].AddrKind != "Public" || reps2[0].Name != "Living" || reps2[0].RSSI != -35 {
		t.Fatalf("unexpected columnar rep 0: %+v", reps2[0])
	}
	if len(reps2[0].AppleTypes) != 1 || reps2[0].AppleTypes[0] != "AirPlay" {
		t.Fatalf("expected AirPlay in rep 0, got %v", reps2[0].AppleTypes)
	}
	if reps2[1].MAC != "C6:55:44:33:22:11" || reps2[1].AddrKind != "Random-St" || reps2[1].RSSI != -72 {
		t.Fatalf("unexpected columnar rep 1: %+v", reps2[1])
	}
	if len(reps2[1].AppleTypes) != 1 || reps2[1].AppleTypes[0] != "FindMy" {
		t.Fatalf("expected FindMy in rep 1, got %v", reps2[1].AppleTypes)
	}
}

func TestServicePresenceLifecycle(t *testing.T) {
	irkHex := "ec0234a357c8ad05341010a60a397d9b"
	addrLE := [6]byte{0xAA, 0xFB, 0x0D, 0x94, 0x81, 0x70}
	staticLE := [6]byte{0x66, 0x55, 0x44, 0x33, 0x22, 0x11}

	var changes []bool
	pings := 0
	svc := NewService(config.BluetoothConfig{
		Enabled:        true,
		RSSIThreshold:  -65,
		AwayTimeoutSec: 10,
		Devices: []config.BLEDeviceConfig{
			{ID: "watch", Name: "Apple Watch", IRK: irkHex, Enabled: true},
		},
	}, false, Callbacks{
		OnPresenceChange: func(present bool, _ string, _ int) {
			changes = append(changes, present)
		},
		OnProximityPing: func(_ string, _ int) {
			pings++
		},
	})

	t0 := time.Now()
	// Far packet (-80 dBm) should be recorded in nearby, not trigger presence.
	svc.ProcessReport(AdvReport{
		AddrLE: addrLE, MAC: FormatMACLE(addrLE), AddrType: 1, AddrKind: "Random-RPA", RSSI: -80, SeenAt: t0,
	})
	if svc.Status().Present {
		t.Fatalf("did not expect -80 dBm to trigger presence")
	}

	// Strong packet (-58 >= -65 + 5) triggers immediate presence.
	svc.ProcessReport(AdvReport{
		AddrLE: addrLE, MAC: FormatMACLE(addrLE), AddrType: 1, AddrKind: "Random-RPA", RSSI: -58, SeenAt: t0.Add(1 * time.Second),
	})
	st := svc.Status()
	if !st.Present || st.MatchedDevice != "Apple Watch" || st.BestRSSI != -58 || len(changes) != 1 || !changes[0] || pings != 1 {
		t.Fatalf("unexpected present state: %+v changes=%v pings=%d", st, changes, pings)
	}

	// Hysteresis (-69 >= -65 - 6) keeps presence alive and fires ping after 2s rate-limit.
	svc.ProcessReport(AdvReport{
		AddrLE: addrLE, MAC: FormatMACLE(addrLE), AddrType: 1, AddrKind: "Random-RPA", RSSI: -69, SeenAt: t0.Add(5 * time.Second),
	})
	svc.EvaluateAway(t0.Add(12 * time.Second))
	if !svc.Status().Present || pings != 2 {
		t.Fatalf("expected hysteresis to refresh lastPresentAt and increment pings, present=%v pings=%d", svc.Status().Present, pings)
	}

	// Silence >= AwayTimeoutSec clears presence.
	svc.EvaluateAway(t0.Add(16 * time.Second))
	if svc.Status().Present || len(changes) != 2 || changes[1] {
		t.Fatalf("expected away state after timeout, present=%v changes=%v", svc.Status().Present, changes)
	}

	// Reconfigure to track a static MAC and require 2 packets at threshold (-63 >= -65 but < -60).
	svc.Reconfigure(config.BluetoothConfig{
		Enabled:        true,
		RSSIThreshold:  -65,
		AwayTimeoutSec: 10,
		Devices: []config.BLEDeviceConfig{
			{ID: "tag", Name: "Key Tag", MAC: FormatMACLE(staticLE), Enabled: true},
		},
	})
	t1 := t0.Add(20 * time.Second)
	svc.ProcessReport(AdvReport{
		AddrLE: staticLE, MAC: FormatMACLE(staticLE), AddrType: 0, AddrKind: "Public", RSSI: -63, SeenAt: t1,
	})
	if svc.Status().Present {
		t.Fatalf("expected first packet below thresh+5 to require confirmation")
	}
	svc.ProcessReport(AdvReport{
		AddrLE: staticLE, MAC: FormatMACLE(staticLE), AddrType: 0, AddrKind: "Public", RSSI: -64, SeenAt: t1.Add(2 * time.Second),
	})
	st = svc.Status()
	if !st.Present || st.MatchedDevice != "Key Tag" {
		t.Fatalf("expected second packet within 5s to confirm static MAC presence, got %+v", st)
	}
}
