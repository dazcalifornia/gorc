package transport

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"sync"
	"time"
)

// Sim is a simulator transport that synthesises devices in-process.
//
// It is not a mock of the layers above it: each simulated device emits the
// same bytes the real hardware puts on the wire, so a session against
// sim:spo2 exercises the production BCI framing and the production GATT
// decoders. That makes the whole pipeline — transport, decode, hub, websocket,
// UI — demonstrable and testable with no hardware attached.
type Sim struct{}

func NewSim() *Sim { return &Sim{} }

func (s *Sim) Kind() Kind { return KindSim }

func (s *Sim) Describe() Availability {
	return Availability{
		Kind: KindSim, Label: "Simulator", Available: true,
		Note: "Synthetic devices emitting real protocol bytes. Use these to prove the pipeline end to end, or to develop against while the hardware is elsewhere.",
	}
}

// simDevice describes one synthetic device.
type simDevice struct {
	id      string
	name    string
	profile string
	note    string
	// run generates traffic until ctx is cancelled.
	run func(ctx context.Context, emit func(Frame))
}

func simDevices() []simDevice {
	return []simDevice{
		{
			id: "spo2", name: "Simulated fingertip pulse oximeter", profile: "spo2-bci",
			note: "BCI 5-byte stream at 60 Hz, including a realistic plethysmogram and an initial finger-search phase",
			run:  runSimBCI,
		},
		{
			id: "scale", name: "Simulated weight + height column", profile: "ascii-lines",
			note: "A&D-style ASCII lines; cycles through unstable and stable weighings every 20 s",
			run:  runSimScale,
		},
		{
			id: "ble-scale", name: "Simulated BLE body-composition scale", profile: "ble-gatt",
			note: "Weight Measurement (0x2A9D) with BMI and height, plus Body Composition (0x2A9C)",
			run:  runSimBLEScale,
		},
		{
			id: "ble-oximeter", name: "Simulated BLE pulse oximeter", profile: "ble-gatt",
			note: "PLX Continuous Measurement (0x2A5F) at 1 Hz with perfusion index, plus battery level",
			run:  runSimBLEOximeter,
		},
		{
			id: "ble-vitals", name: "Simulated BLE thermometer + blood pressure", profile: "ble-gatt",
			note: "Health Thermometer (0x2A1C) and Blood Pressure (0x2A35) spot measurements",
			run:  runSimBLEVitals,
		},
	}
}

func (s *Sim) Scan(ctx context.Context) ([]DeviceInfo, error) {
	out := []DeviceInfo{}
	for _, d := range simDevices() {
		out = append(out, DeviceInfo{
			ID:               "sim:" + d.id,
			Kind:             KindSim,
			Name:             d.name,
			Address:          d.id,
			Details:          map[string]string{"emits": d.note},
			SuggestedProfile: d.profile,
		})
	}
	return out, nil
}

func (s *Sim) Open(ctx context.Context, dev DeviceInfo, opts OpenOptions) (Conn, error) {
	for _, d := range simDevices() {
		if d.id != dev.Address {
			continue
		}
		runCtx, cancel := context.WithCancel(context.Background())
		c := &simConn{dev: dev, frames: make(chan Frame, 256), cancel: cancel}
		go func() {
			defer close(c.frames)
			d.run(runCtx, c.emit)
		}()
		return c, nil
	}
	return nil, fmt.Errorf("unknown simulated device %q", dev.Address)
}

type simConn struct {
	dev    DeviceInfo
	frames chan Frame
	cancel context.CancelFunc

	mu     sync.Mutex
	closed bool
}

func (c *simConn) Device() DeviceInfo   { return c.dev }
func (c *simConn) Frames() <-chan Frame { return c.frames }
func (c *simConn) Err() error           { return nil }

func (c *simConn) emit(f Frame) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return
	}
	select {
	case c.frames <- f:
	default: // consumer fell behind; drop like real hardware would
	}
}

// Write accepts and discards commands. Real devices mostly ignore the wake-up
// bytes too, which is why the BCI decoder sends them unconditionally.
func (c *simConn) Write(p []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("connection closed")
	}
	return nil
}

func (c *simConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.cancel()
	return nil
}

// --- generators -----------------------------------------------------------

// runSimBCI emits the 5-byte BCI frames of a fingertip oximeter at 60 Hz.
func runSimBCI(ctx context.Context, emit func(Frame)) {
	const hz = 60
	tick := time.NewTicker(time.Second / hz)
	defer tick.Stop()

	start := time.Now()
	phase := 0.0
	spo2, pulse := 98.0, 72.0

	for n := 0; ; n++ {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}

		elapsed := time.Since(start).Seconds()
		// Spend the first three seconds searching for a finger, the way a real
		// probe does, so the UI's quality states are exercised.
		searching := elapsed < 3

		// Drift the scalars slowly and plausibly.
		if n%hz == 0 {
			pulse = clamp(pulse+rand.NormFloat64()*1.5, 58, 88)
			spo2 = clamp(spo2+rand.NormFloat64()*0.4, 95, 100)
		}

		phase += pulse / 60 / hz
		pleth := 0
		signal := 0
		if !searching {
			pleth = int(ppg(phase) * 127)
			signal = 7 + n%2
		}

		b0 := byte(0x80 | signal&0x0F)
		if searching {
			b0 |= 0x10 // searching too long
		}
		pr, sp := int(pulse), int(spo2)
		if searching {
			pr, sp = 0, 127 // protocol sentinels for "no reading"
		}
		b2 := byte(0x0A)
		if pr&0x80 != 0 {
			b2 |= 0x40
		}
		emit(Frame{At: time.Now(), Source: "rx", Data: []byte{
			b0, byte(pleth & 0x7F), b2, byte(pr & 0x7F), byte(sp & 0x7F),
		}})
	}
}

// ppg approximates a photoplethysmogram over one cardiac cycle: a steep
// systolic upstroke, a dicrotic notch, then diastolic decay. Normalised 0..1.
func ppg(phase float64) float64 {
	t := phase - math.Floor(phase)
	systolic := math.Exp(-math.Pow((t-0.18)/0.11, 2))
	dicrotic := 0.35 * math.Exp(-math.Pow((t-0.42)/0.13, 2))
	return clamp((systolic+dicrotic)*0.92, 0, 1)
}

// runSimScale emits A&D-style ASCII weight lines, cycling between an empty
// platform, an unstable weighing and a settled reading.
func runSimScale(ctx context.Context, emit func(Frame)) {
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()

	start := time.Now()
	target := 70.0 + rand.Float64()*20
	height := 160.0 + rand.Float64()*25

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}

		// A 20-second cycle: step on, wobble, settle, step off.
		cycle := math.Mod(time.Since(start).Seconds(), 20)
		var line string
		switch {
		case cycle < 2:
			line = "ST,GS,+    0.00 kg\r\n"
		case cycle < 6:
			wobble := target + rand.NormFloat64()*1.2
			line = fmt.Sprintf("US,GS,+%8.2f kg\r\n", wobble)
		case cycle < 14:
			settled := target + rand.NormFloat64()*0.02
			line = fmt.Sprintf("ST,GS,+%8.2f kg\r\n", settled)
			// Height columns interleave a height line once settled.
			if cycle > 8 && cycle < 9 {
				line += fmt.Sprintf("HT:%.1f BMI:%.1f\r\n", height, target/math.Pow(height/100, 2))
			}
		default:
			line = "ST,GS,+    0.00 kg\r\n"
			if cycle > 19.5 {
				target = 70.0 + rand.Float64()*20
				height = 160.0 + rand.Float64()*25
			}
		}
		emit(Frame{At: time.Now(), Source: "rx", Data: []byte(line)})
	}
}

// runSimBLEScale emits Weight Scale and Body Composition notifications.
func runSimBLEScale(ctx context.Context, emit func(Frame)) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()

	weight := 72.4
	height := 1.712
	for {
		weight = clamp(weight+rand.NormFloat64()*0.3, 45, 140)
		bmi := weight / (height * height)
		fat := clamp(18+rand.NormFloat64()*2, 5, 45)
		lean := weight * (1 - fat/100)

		// 0x2A9D: flags bit3 = BMI and height present, SI units.
		wm := []byte{0x08}
		wm = append(wm, u16le(uint16(math.Round(weight/0.005)))...)
		wm = append(wm, u16le(uint16(math.Round(bmi*10)))...)
		wm = append(wm, u16le(uint16(math.Round(height/0.001)))...)
		emit(Frame{At: time.Now(), Source: "00002a9d-0000-1000-8000-00805f9b34fb", Data: wm})

		// 0x2A9C: flags bit6 = fat free mass, bit10 = weight, bit11 = height.
		bc := u16le(0x0040 | 0x0400 | 0x0800)
		bc = append(bc, u16le(uint16(math.Round(fat*10)))...)
		bc = append(bc, u16le(uint16(math.Round(lean/0.005)))...)
		bc = append(bc, u16le(uint16(math.Round(weight/0.005)))...)
		bc = append(bc, u16le(uint16(math.Round(height*1000)))...)
		emit(Frame{At: time.Now(), Source: "00002a9c-0000-1000-8000-00805f9b34fb", Data: bc})

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// runSimBLEOximeter emits PLX Continuous Measurement at 1 Hz plus battery.
func runSimBLEOximeter(ctx context.Context, emit func(Frame)) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()

	spo2, pulse, battery := 97.0, 70.0, 92.0
	for n := 0; ; n++ {
		spo2 = clamp(spo2+rand.NormFloat64()*0.4, 93, 100)
		pulse = clamp(pulse+rand.NormFloat64()*2, 55, 95)
		pai := clamp(3+rand.NormFloat64()*0.5, 0.2, 20)

		// flags bit4 = perfusion index present.
		p := []byte{0x10}
		p = append(p, sfloatBytes(spo2, -1)...)
		p = append(p, sfloatBytes(pulse, -1)...)
		p = append(p, sfloatBytes(pai, -1)...)
		emit(Frame{At: time.Now(), Source: "00002a5f-0000-1000-8000-00805f9b34fb", Data: p})

		if n%30 == 0 {
			emit(Frame{At: time.Now(), Source: "00002a19-0000-1000-8000-00805f9b34fb", Data: []byte{byte(battery)}})
			battery = clamp(battery-0.5, 1, 100)
		}

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// runSimBLEVitals emits thermometer and blood-pressure spot measurements.
func runSimBLEVitals(ctx context.Context, emit func(Frame)) {
	tick := time.NewTicker(8 * time.Second)
	defer tick.Stop()

	for n := 0; ; n++ {
		if n%2 == 0 {
			temp := clamp(36.8+rand.NormFloat64()*0.3, 35, 40)
			// 0x2A1C: flags 0 = Celsius, FLOAT32 with exponent -2.
			f := []byte{0x00}
			f = append(f, float32medBytes(temp, -2)...)
			emit(Frame{At: time.Now(), Source: "00002a1c-0000-1000-8000-00805f9b34fb", Data: f})
		} else {
			sys := clamp(120+rand.NormFloat64()*8, 90, 180)
			dia := clamp(78+rand.NormFloat64()*6, 50, 110)
			mean := dia + (sys-dia)/3
			pulse := clamp(72+rand.NormFloat64()*6, 50, 110)
			// 0x2A35: flags bit2 = pulse rate present, mmHg.
			f := []byte{0x04}
			f = append(f, sfloatBytes(sys, 0)...)
			f = append(f, sfloatBytes(dia, 0)...)
			f = append(f, sfloatBytes(mean, 0)...)
			f = append(f, sfloatBytes(pulse, 0)...)
			emit(Frame{At: time.Now(), Source: "00002a35-0000-1000-8000-00805f9b34fb", Data: f})
		}

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// --- wire-format encoders (mirror the decoders in internal/decode) --------

func u16le(v uint16) []byte { return []byte{byte(v), byte(v >> 8)} }

// sfloatBytes encodes an IEEE-11073 16-bit SFLOAT at the given base-10
// exponent.
func sfloatBytes(v float64, exp int) []byte {
	mantissa := int16(math.Round(v / math.Pow10(exp)))
	raw := uint16(exp&0x0F)<<12 | uint16(mantissa)&0x0FFF
	return u16le(raw)
}

// float32medBytes encodes an IEEE-11073 32-bit FLOAT at the given exponent.
func float32medBytes(v float64, exp int) []byte {
	mantissa := int32(math.Round(v / math.Pow10(exp)))
	raw := uint32(uint8(exp))<<24 | uint32(mantissa)&0x00FFFFFF
	return []byte{byte(raw), byte(raw >> 8), byte(raw >> 16), byte(raw >> 24)}
}

func clamp(v, lo, hi float64) float64 { return math.Min(math.Max(v, lo), hi) }
