// Package decode turns raw device bytes into normalised Readings.
//
// A Decoder is stateful and single-goroutine: the session pump owns it and
// calls Feed for every frame in arrival order. Byte-stream decoders buffer
// partial frames across calls; GATT decoders are effectively stateless but
// use the same interface so the pump does not care which it holds.
package decode

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/expert/go-connector/internal/model"
	"github.com/expert/go-connector/internal/transport"
)

// Decoder converts frames to readings.
type Decoder interface {
	// Feed consumes one frame and returns any readings it completed.
	Feed(f transport.Frame) []model.Reading
}

// Primer is implemented by decoders whose device needs to be told to start
// talking, or kept awake. The session pump calls these if present.
type Primer interface {
	// OnOpen returns command payloads to write immediately after connect.
	OnOpen() [][]byte
	// Keepalive returns a payload and interval, or a nil payload for none.
	Keepalive() ([]byte, time.Duration)
}

// Profile describes a decoder to the UI.
type Profile struct {
	ID         string           `json:"id"`
	Label      string           `json:"label"`
	Note       string           `json:"note"`
	Transports []transport.Kind `json:"transports"`
	// DefaultBaud is offered in the UI for byte-stream transports.
	DefaultBaud int `json:"defaultBaud,omitempty"`
}

type registration struct {
	Profile Profile
	New     func() Decoder
}

var registry = map[string]registration{}

func register(p Profile, newFn func() Decoder) {
	registry[p.ID] = registration{Profile: p, New: newFn}
}

// New builds a fresh decoder for a profile id.
func New(id string) (Decoder, error) {
	r, ok := registry[id]
	if !ok {
		return nil, fmt.Errorf("unknown decoder profile %q", id)
	}
	return r.New(), nil
}

// Profiles lists registered profiles, sorted for a stable UI order.
func Profiles() []Profile {
	out := make([]Profile, 0, len(registry))
	for _, r := range registry {
		out = append(out, r.Profile)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ---------- IEEE 11073-20601 numeric types used throughout Bluetooth SIG
// health characteristics. These are decimal floats: a signed mantissa and a
// base-10 exponent, with a handful of reserved bit patterns for NaN/±Inf.

const (
	sfloatNaN  = 0x07FF
	sfloatInf  = 0x07FE
	sfloatNInf = 0x0802
	sfloatNRes = 0x0800
	sfloatRsvd = 0x0801
)

// sfloat16 decodes a 16-bit IEEE-11073 SFLOAT (little endian).
// Returns NaN for the reserved "no value" patterns so callers can skip them.
func sfloat16(b []byte) float64 {
	if len(b) < 2 {
		return math.NaN()
	}
	raw := uint16(b[0]) | uint16(b[1])<<8
	mantissa := int32(raw & 0x0FFF)
	exponent := int8(raw >> 12)

	switch mantissa {
	case sfloatNaN, sfloatNRes, sfloatRsvd:
		return math.NaN()
	case sfloatInf:
		return math.Inf(1)
	case sfloatNInf:
		return math.Inf(-1)
	}
	if mantissa >= 0x0800 { // sign-extend 12-bit two's complement
		mantissa -= 0x1000
	}
	if exponent >= 0x08 { // sign-extend 4-bit two's complement
		exponent -= 0x10
	}
	return float64(mantissa) * math.Pow10(int(exponent))
}

// float32med decodes a 32-bit IEEE-11073 FLOAT (little endian): 24-bit signed
// mantissa plus 8-bit signed exponent. Used by Health Thermometer.
func float32med(b []byte) float64 {
	if len(b) < 4 {
		return math.NaN()
	}
	raw := uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
	mantissa := int32(raw & 0x00FFFFFF)
	exponent := int8(raw >> 24)

	switch mantissa {
	case 0x007FFFFF, 0x00800000, 0x00800001: // NaN, NRes, reserved
		return math.NaN()
	case 0x007FFFFE:
		return math.Inf(1)
	case 0x00800002:
		return math.Inf(-1)
	}
	if mantissa >= 0x00800000 {
		mantissa -= 0x01000000
	}
	return float64(mantissa) * math.Pow10(int(exponent))
}

// round2 trims float noise introduced by unit scaling so the UI and the JSON
// payload show 70.55 rather than 70.55000000000001.
func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
