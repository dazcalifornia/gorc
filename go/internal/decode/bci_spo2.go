package decode

import (
	"time"

	"github.com/expert/go-connector/internal/model"
	"github.com/expert/go-connector/internal/transport"
)

func init() {
	register(Profile{
		ID:          "spo2-bci",
		Label:       "Pulse oximeter — BCI 5-byte stream",
		Note:        "Contec CMS50D+/E/F, Berry BM1000C, Jumper JPD-500 and other BCI-protocol fingertip oximeters. Streams SpO₂, pulse rate and plethysmogram continuously at ~60 Hz.",
		Transports:  []transport.Kind{transport.KindSerial, transport.KindBTClassic, transport.KindSim},
		DefaultBaud: 19200,
	}, func() Decoder { return &bciSpO2{} })
}

// bciSpO2 decodes the "BCI protocol" live stream shared by most cheap
// fingertip pulse oximeters.
//
// The wire format is a 5-byte frame where only the first byte has bit 7 set,
// which is also how we resynchronise after dropped bytes:
//
//	b0: 1 S S S  N T G G    bit7 sync, bit6 pulse beep, bit5 no sensor,
//	                        bit4 searching-too-long, bits0-3 signal strength
//	b1: 0 P P P  P P P P    7-bit plethysmogram sample (0-127)
//	b2: 0 R B B  B B B B    bit6 = high bit of pulse rate, bits0-5 bar graph
//	b3: 0 R R R  R R R R    low 7 bits of pulse rate
//	b4: 0 O O O  O O O O    SpO₂ percent (127 = invalid)
type bciSpO2 struct {
	buf []byte

	lastSpO2   float64
	lastPulse  float64
	lastSignal float64
	lastEmit   time.Time
}

// Most BCI oximeters stream as soon as they power on, but the Contec CMS50E/F
// family stays idle until asked for real-time data and sleeps again without a
// periodic poke. Both commands are ignored by devices that just stream.
var (
	bciStartRealtime = []byte{0x7D, 0x81, 0xA1, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80}
	bciStayLive      = []byte{0xA7}
)

func (d *bciSpO2) OnOpen() [][]byte { return [][]byte{bciStartRealtime} }

func (d *bciSpO2) Keepalive() ([]byte, time.Duration) { return bciStayLive, time.Second }

func (d *bciSpO2) Feed(f transport.Frame) []model.Reading {
	d.buf = append(d.buf, f.Data...)
	var out []model.Reading

	for {
		// Resynchronise: a frame must start on a byte with bit 7 set.
		start := -1
		for i, b := range d.buf {
			if b&0x80 != 0 {
				start = i
				break
			}
		}
		if start < 0 {
			d.buf = d.buf[:0] // nothing framable, drop the garbage
			return out
		}
		if start > 0 {
			d.buf = d.buf[start:]
		}
		if len(d.buf) < 5 {
			return out
		}
		frame := d.buf[:5]
		// All continuation bytes must have bit 7 clear. If not, we latched onto
		// a payload byte that happened to look like a sync byte: skip one and
		// try again rather than emitting a scrambled reading.
		if frame[1]&0x80 != 0 || frame[2]&0x80 != 0 || frame[3]&0x80 != 0 || frame[4]&0x80 != 0 {
			d.buf = d.buf[1:]
			continue
		}
		d.buf = d.buf[5:]
		out = append(out, d.frame(frame, f.At)...)
	}
}

func (d *bciSpO2) frame(b []byte, at time.Time) []model.Reading {
	signal := float64(b[0] & 0x0F)
	noSensor := b[0]&0x20 != 0
	searching := b[0]&0x10 != 0

	pleth := float64(b[1] & 0x7F)
	pulse := float64(uint16(b[2]&0x40)<<1 | uint16(b[3]&0x7F))
	spo2 := float64(b[4] & 0x7F)

	quality := model.QualityOK
	switch {
	case noSensor:
		quality = model.QualityNoSensor
	case searching:
		quality = model.QualitySearching
	}

	out := []model.Reading{{
		Metric:   model.MetricPleth,
		Value:    pleth,
		Unit:     "",
		Quality:  quality,
		At:       at,
		Waveform: true,
	}}

	// 60 frames a second of an unchanged SpO2 is noise. Emit scalars only when
	// they move, with a 1 s heartbeat so a client that connects mid-session
	// still sees current values.
	heartbeat := at.Sub(d.lastEmit) >= time.Second
	changed := spo2 != d.lastSpO2 || pulse != d.lastPulse || signal != d.lastSignal
	if !heartbeat && !changed {
		return out
	}
	d.lastEmit, d.lastSpO2, d.lastPulse, d.lastSignal = at, spo2, pulse, signal

	if spo2 > 0 && spo2 <= 100 {
		out = append(out, model.Reading{
			Metric: model.MetricSpO2, Value: spo2, Unit: "%", Quality: quality, At: at,
		})
	}
	if pulse > 0 && pulse < 255 {
		out = append(out, model.Reading{
			Metric: model.MetricPulseRate, Value: pulse, Unit: "bpm", Quality: quality, At: at,
		})
	}
	out = append(out, model.Reading{
		Metric: model.MetricSignalStrength, Value: signal, Unit: "/15", Quality: quality, At: at,
	})
	return out
}
