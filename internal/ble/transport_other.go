//go:build !linux

package ble

import "context"

func openHardwareTransport(_ context.Context) (hciTransport, error) {
	return nil, errHardwareUnavailable
}
