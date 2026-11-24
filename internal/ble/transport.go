package ble

import (
	"errors"
	"time"
)

var (
	errReadTimeout         = errors.New("hci read timeout")
	errHardwareUnavailable = errors.New("bluetooth hardware transport unavailable")
)

type hciTransport interface {
	BackendName() string
	ReadEvent(timeout time.Duration) (evtCode byte, payload []byte, err error)
	Close() error
}
