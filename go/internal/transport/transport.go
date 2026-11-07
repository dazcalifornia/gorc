// Package transport abstracts the physical link to a device. Everything above
// this layer sees only DeviceInfo and a stream of Frames, so a BLE pulse
// oximeter and an RS-232 scale travel the same path through the app.
package transport

import (
	"context"
	"errors"
	"time"
)

// Kind is the physical link type.
type Kind string

const (
	KindSerial    Kind = "serial"     // USB CDC / FTDI / RS-232-over-USB
	KindBLE       Kind = "ble"        // Bluetooth Low Energy (GATT)
	KindBTClassic Kind = "bt-classic" // Classic Bluetooth SPP / RFCOMM
	KindSim       Kind = "sim"        // built-in simulator, no hardware needed
)

// ErrUnsupported is returned by a transport that cannot run on this platform.
var ErrUnsupported = errors.New("transport not supported on this platform")

// DeviceInfo is a discovered device. ID is stable within a transport and is
// what the frontend passes back when opening a session.
type DeviceInfo struct {
	ID      string            `json:"id"`
	Kind    Kind              `json:"kind"`
	Name    string            `json:"name"`
	Address string            `json:"address"`
	RSSI    int16             `json:"rssi,omitempty"`
	Details map[string]string `json:"details,omitempty"`
	// Services lists advertised GATT service UUIDs (BLE only). The UI uses it
	// to suggest a decoder profile.
	Services []string `json:"services,omitempty"`
	// SuggestedProfile is the decoder the transport thinks fits, if any.
	SuggestedProfile string `json:"suggestedProfile,omitempty"`
}

// Frame is one chunk of bytes received from a device.
//
// For byte-stream links (serial, RFCOMM) Source is "rx" and Data is whatever
// arrived from the port — framing is the decoder's problem. For BLE, one Frame
// is exactly one GATT notification and Source is the characteristic UUID,
// which is what tells the GATT decoder how to read the bytes.
type Frame struct {
	At     time.Time `json:"at"`
	Source string    `json:"source"`
	Data   []byte    `json:"data"`
}

// OpenOptions carries link-specific knobs from the API request.
type OpenOptions struct {
	BaudRate int    `json:"baudRate,omitempty"`
	Profile  string `json:"profile,omitempty"`
}

// Conn is a live link to one device.
type Conn interface {
	Device() DeviceInfo
	// Frames is closed when the link drops; Err then reports why.
	Frames() <-chan Frame
	// Write sends bytes back to the device (wake-up commands, keepalives).
	// BLE writes go to the connection's designated write characteristic.
	Write(p []byte) error
	Err() error
	Close() error
}

// Transport discovers and opens devices of one Kind.
type Transport interface {
	Kind() Kind
	// Describe reports whether this transport can run here, and why not if it
	// cannot, so the UI can grey out a tab instead of failing on click.
	Describe() Availability
	Scan(ctx context.Context) ([]DeviceInfo, error)
	Open(ctx context.Context, dev DeviceInfo, opts OpenOptions) (Conn, error)
}

// Availability is the self-report shown in the UI's transport picker.
type Availability struct {
	Kind      Kind   `json:"kind"`
	Label     string `json:"label"`
	Available bool   `json:"available"`
	Note      string `json:"note"`
}
