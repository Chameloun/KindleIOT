package ble

import (
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

// AdvReport represents a single parsed HCI_LE_Advertising_Report entry.
type AdvReport struct {
	AddrLE     [6]byte
	MAC        string
	AddrType   byte
	AddrKind   string
	RSSI       int
	Name       string
	AppleTypes []string
	SeenAt     time.Time
}

// ParseAdvertisingReport decodes an HCI LE Advertising Report subevent payload (params starting at subevent byte 0x02).
func ParseAdvertisingReport(params []byte, now time.Time) []AdvReport {
	if len(params) < 2 || params[0] != 0x02 {
		return nil
	}
	n := int(params[1])
	if n <= 0 {
		return nil
	}
	// Columnar layout: Event_Type[N], Address_Type[N], Address[6N], Length_Data[N], Data[sum], RSSI[N].
	minBase := 2 + 10*n
	if len(params) < minBase {
		return nil
	}

	addrTypeOff := 2 + n
	addrOff := 2 + 2*n
	lenOff := 2 + 8*n
	dataOff := 2 + 9*n

	totalDataLen := 0
	for i := 0; i < n; i++ {
		totalDataLen += int(params[lenOff+i])
	}
	rssiOff := dataOff + totalDataLen
	if len(params) < rssiOff+n {
		return nil
	}

	reports := make([]AdvReport, 0, n)
	curData := dataOff
	for i := 0; i < n; i++ {
		addrType := params[addrTypeOff+i]
		var addrLE [6]byte
		copy(addrLE[:], params[addrOff+6*i:addrOff+6*(i+1)])
		dlen := int(params[lenOff+i])
		adData := params[curData : curData+dlen]
		curData += dlen
		rssi := int(int8(params[rssiOff+i]))

		name, appleTypes := ParseAD(adData)
		reports = append(reports, AdvReport{
			AddrLE:     addrLE,
			MAC:        FormatMACLE(addrLE),
			AddrType:   addrType,
			AddrKind:   DescribeAddrKind(addrType, addrLE[5]),
			RSSI:       rssi,
			Name:       name,
			AppleTypes: appleTypes,
			SeenAt:     now,
		})
	}
	return reports
}

// ParseAD extracts a printable local name and Apple Continuity sub-types from raw AD bytes.
func ParseAD(data []byte) (string, []string) {
	var name string
	var appleTypes []string
	seenApple := make(map[string]struct{})
	off := 0
	for off < len(data) {
		l := int(data[off])
		if l == 0 {
			break
		}
		end := off + 1 + l
		if end > len(data) {
			break
		}
		if l >= 1 {
			adType := data[off+1]
			val := data[off+2 : end]
			switch adType {
			case 0x08, 0x09:
				if cand := strings.TrimSpace(string(val)); cand != "" && isPrintableText(cand) {
					name = cand
				}
			case 0xFF:
				if len(val) >= 2 && binary.LittleEndian.Uint16(val[:2]) == 0x004C {
					j := 2
					for j+1 < len(val) {
						subType := appleSubTypeName(val[j])
						subLen := int(val[j+1])
						if _, ok := seenApple[subType]; !ok {
							seenApple[subType] = struct{}{}
							appleTypes = append(appleTypes, subType)
						}
						if subLen <= 0 || j+2+subLen > len(val) {
							break
						}
						j += 2 + subLen
					}
					if len(appleTypes) == 0 {
						appleTypes = append(appleTypes, "AppleMfg")
					}
				}
			}
		}
		off = end
	}
	return name, appleTypes
}

func appleSubTypeName(t byte) string {
	switch t {
	case 0x10:
		return "NearbyInfo"
	case 0x0C:
		return "Handoff"
	case 0x07:
		return "ProximityPairing"
	case 0x12:
		return "FindMy"
	case 0x09:
		return "AirPlay"
	case 0x05:
		return "AirDrop"
	default:
		return fmt.Sprintf("0x%02X", t)
	}
}

// DescribeAddrKind returns a short classification string for a BLE advertising address type.
func DescribeAddrKind(addrType, msb byte) string {
	switch addrType {
	case 0:
		return "Public"
	case 1:
		switch msb & 0xC0 {
		case 0x40:
			return "Random-RPA"
		case 0xC0:
			return "Random-St"
		default:
			return "Random-NR"
		}
	default:
		return fmt.Sprintf("Type-%d", addrType)
	}
}

// FormatMACLE formats a 6-byte little-endian HCI address in canonical MSB colon notation.
func FormatMACLE(le [6]byte) string {
	return fmt.Sprintf("%02X:%02X:%02X:%02X:%02X:%02X",
		le[5], le[4], le[3], le[2], le[1], le[0])
}

func isPrintableText(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7F {
			return false
		}
	}
	return true
}
