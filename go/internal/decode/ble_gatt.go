package decode

import (
	"math"
	"time"

	"github.com/expert/go-connector/internal/btsig"

	"github.com/expert/go-connector/internal/model"
	"github.com/expert/go-connector/internal/transport"
)

func init() {
	register(Profile{
		ID:         "ble-gatt",
		Label:      "BLE — Bluetooth SIG health profiles",
		Note:       "Standards-based BLE devices: Pulse Oximeter (0x1822), Weight Scale (0x181D), Body Composition (0x181B), Health Thermometer (0x1809), Blood Pressure (0x1810) and Heart Rate (0x180D). Decodes IEEE-11073 floats; no vendor code needed.",
		Transports: []transport.Kind{transport.KindBLE, transport.KindSim},
	}, func() Decoder { return &bleGATT{} })
}

// Bluetooth SIG 16-bit characteristic UUIDs we understand.
const (
	charPLXSpotCheck      = 0x2A5E
	charPLXContinuous     = 0x2A5F
	charWeightMeasurement = 0x2A9D
	charBodyComposition   = 0x2A9C
	charTemperature       = 0x2A1C
	charBloodPressure     = 0x2A35
	charHeartRate         = 0x2A37
	charBatteryLevel      = 0x2A19
	charWeightSimple      = 0x2A98
	charHeightSimple      = 0x2A8E
)

// bleGATT decodes Bluetooth SIG health characteristics. One Frame is one GATT
// notification, and Frame.Source carries the characteristic UUID that says how
// to read it, so this decoder needs no cross-frame state.
type bleGATT struct{}

func (d *bleGATT) Feed(f transport.Frame) []model.Reading {
	short, ok := btsig.ShortUUID(f.Source)
	if !ok {
		return nil
	}
	c := &cursor{b: f.Data}
	at := f.At

	switch short {
	case charPLXContinuous:
		return plxContinuous(c, at)
	case charPLXSpotCheck:
		return plxSpotCheck(c, at)
	case charWeightMeasurement:
		return weightMeasurement(c, at)
	case charBodyComposition:
		return bodyComposition(c, at)
	case charTemperature:
		return temperature(c, at)
	case charBloodPressure:
		return bloodPressure(c, at)
	case charHeartRate:
		return heartRate(c, at)
	case charBatteryLevel:
		if v, ok := c.u8(); ok {
			return []model.Reading{{Metric: model.MetricBattery, Value: float64(v), Unit: "%", Quality: model.QualityOK, At: at}}
		}
	case charWeightSimple:
		if v, ok := c.u16(); ok && v != 0xFFFF {
			return []model.Reading{{Metric: model.MetricWeight, Value: round2(float64(v) * 0.005), Unit: "kg", Quality: model.QualityOK, At: at}}
		}
	case charHeightSimple:
		if v, ok := c.u16(); ok && v != 0xFFFF {
			return []model.Reading{{Metric: model.MetricHeight, Value: round2(float64(v)), Unit: "cm", Quality: model.QualityOK, At: at}}
		}
	}
	return nil
}

// --- Pulse Oximeter Service (0x1822) -------------------------------------

func plxContinuous(c *cursor, at time.Time) []model.Reading {
	flags, ok := c.u8()
	if !ok {
		return nil
	}
	var out []model.Reading
	// SpO2PR-Normal is mandatory; the Fast and Slow variants are the same pair
	// of fields averaged over different windows, so we only surface Normal and
	// skip past the others to reach the perfusion index.
	out = append(out, spo2Pair(c, at)...)
	if flags&0x01 != 0 {
		c.skip(4) // SpO2PR-Fast
	}
	if flags&0x02 != 0 {
		c.skip(4) // SpO2PR-Slow
	}
	quality := model.QualityOK
	if flags&0x04 != 0 {
		if st, ok := c.u16(); ok {
			quality = plxQuality(st)
		}
	}
	if flags&0x08 != 0 {
		c.skip(3) // device and sensor status
	}
	if flags&0x10 != 0 {
		if pai := c.sfloat(); !math.IsNaN(pai) {
			out = append(out, model.Reading{Metric: model.MetricPerfusionIndex, Value: round2(pai), Unit: "%", Quality: quality, At: at})
		}
	}
	for i := range out {
		out[i].Quality = quality
	}
	return out
}

func plxSpotCheck(c *cursor, at time.Time) []model.Reading {
	flags, ok := c.u8()
	if !ok {
		return nil
	}
	out := spo2Pair(c, at)
	if flags&0x01 != 0 {
		if ts, ok := c.timestamp(); ok {
			for i := range out {
				out[i].At = ts
			}
		}
	}
	quality := model.QualityOK
	if flags&0x02 != 0 {
		if st, ok := c.u16(); ok {
			quality = plxQuality(st)
		}
	}
	if flags&0x04 != 0 {
		c.skip(3)
	}
	if flags&0x08 != 0 {
		if pai := c.sfloat(); !math.IsNaN(pai) {
			out = append(out, model.Reading{Metric: model.MetricPerfusionIndex, Value: round2(pai), Unit: "%", Quality: quality, At: at})
		}
	}
	for i := range out {
		out[i].Quality = quality
	}
	return out
}

// spo2Pair reads an SpO2PR field: two consecutive SFLOATs.
func spo2Pair(c *cursor, at time.Time) []model.Reading {
	spo2 := c.sfloat()
	pr := c.sfloat()
	var out []model.Reading
	if !math.IsNaN(spo2) && !math.IsInf(spo2, 0) {
		out = append(out, model.Reading{Metric: model.MetricSpO2, Value: round2(spo2), Unit: "%", Quality: model.QualityOK, At: at})
	}
	if !math.IsNaN(pr) && !math.IsInf(pr, 0) {
		out = append(out, model.Reading{Metric: model.MetricPulseRate, Value: round2(pr), Unit: "bpm", Quality: model.QualityOK, At: at})
	}
	return out
}

// plxQuality maps the PLX Measurement Status bitfield onto our coarse quality
// flag. Bits 5-6 are "measurement ongoing" / "early estimate"; bit 8 is
// "invalid measurement detected"; bit 9 is "questionable".
func plxQuality(status uint16) model.Quality {
	switch {
	case status&(1<<8) != 0:
		return model.QualityArtifact
	case status&(1<<9) != 0:
		return model.QualityArtifact
	case status&(1<<6) != 0:
		return model.QualitySearching
	}
	return model.QualityOK
}

// --- Weight Scale (0x181D) ------------------------------------------------

func weightMeasurement(c *cursor, at time.Time) []model.Reading {
	flags, ok := c.u8()
	if !ok {
		return nil
	}
	imperial := flags&0x01 != 0
	massRes, massUnit := 0.005, "kg"
	lenRes, lenUnit := 100.0, "cm" // metres -> cm, 0.001 m resolution
	if imperial {
		massRes, massUnit = 0.01, "lb"
		lenRes, lenUnit = 0.1, "in"
	}

	var out []model.Reading
	raw, ok := c.u16()
	if !ok {
		return nil
	}
	if raw != 0xFFFF {
		out = append(out, model.Reading{Metric: model.MetricWeight, Value: round2(float64(raw) * massRes), Unit: massUnit, Quality: model.QualityOK, At: at})
	}
	if flags&0x02 != 0 {
		if ts, ok := c.timestamp(); ok {
			at = ts
			for i := range out {
				out[i].At = ts
			}
		}
	}
	if flags&0x04 != 0 {
		c.skip(1) // user id
	}
	if flags&0x08 != 0 {
		if bmi, ok := c.u16(); ok && bmi != 0xFFFF {
			out = append(out, model.Reading{Metric: model.MetricBMI, Value: round2(float64(bmi) * 0.1), Unit: "kg/m²", Quality: model.QualityOK, At: at})
		}
		if h, ok := c.u16(); ok && h != 0xFFFF {
			v := float64(h) * 0.001 * lenRes
			if imperial {
				v = float64(h) * lenRes
			}
			out = append(out, model.Reading{Metric: model.MetricHeight, Value: round2(v), Unit: lenUnit, Quality: model.QualityOK, At: at})
		}
	}
	return out
}

// --- Body Composition (0x181B) -------------------------------------------

func bodyComposition(c *cursor, at time.Time) []model.Reading {
	flags, ok := c.u16()
	if !ok {
		return nil
	}
	imperial := flags&0x0001 != 0
	massRes, massUnit := 0.005, "kg"
	if imperial {
		massRes, massUnit = 0.01, "lb"
	}

	var out []model.Reading
	if fat, ok := c.u16(); ok && fat != 0xFFFF {
		out = append(out, model.Reading{Metric: model.MetricBodyFat, Value: round2(float64(fat) * 0.1), Unit: "%", Quality: model.QualityOK, At: at})
	}
	if flags&0x0002 != 0 {
		if ts, ok := c.timestamp(); ok {
			at = ts
			for i := range out {
				out[i].At = ts
			}
		}
	}
	if flags&0x0004 != 0 {
		c.skip(1) // user id
	}
	if flags&0x0008 != 0 {
		c.skip(2) // basal metabolism, kJ
	}
	if flags&0x0010 != 0 {
		c.skip(2) // muscle percentage
	}
	if flags&0x0020 != 0 {
		c.skip(2) // muscle mass
	}
	if flags&0x0040 != 0 {
		// Fat free mass is the clinically useful one of the mass fields.
		if v, ok := c.u16(); ok && v != 0xFFFF {
			out = append(out, model.Reading{Metric: model.MetricLeanMass, Value: round2(float64(v) * massRes), Unit: massUnit, Quality: model.QualityOK, At: at})
		}
	}
	if flags&0x0080 != 0 {
		c.skip(2) // soft lean mass
	}
	if flags&0x0100 != 0 {
		c.skip(2) // body water mass
	}
	if flags&0x0200 != 0 {
		c.skip(2) // impedance
	}
	if flags&0x0400 != 0 {
		if v, ok := c.u16(); ok && v != 0xFFFF {
			out = append(out, model.Reading{Metric: model.MetricWeight, Value: round2(float64(v) * massRes), Unit: massUnit, Quality: model.QualityOK, At: at})
		}
	}
	if flags&0x0800 != 0 {
		if v, ok := c.u16(); ok && v != 0xFFFF {
			value, unit := float64(v)*0.1, "cm" // 0.001 m -> cm
			if imperial {
				value, unit = float64(v)*0.1, "in"
			}
			out = append(out, model.Reading{Metric: model.MetricHeight, Value: round2(value), Unit: unit, Quality: model.QualityOK, At: at})
		}
	}
	return out
}

// --- Health Thermometer (0x1809) -----------------------------------------

func temperature(c *cursor, at time.Time) []model.Reading {
	flags, ok := c.u8()
	if !ok {
		return nil
	}
	v := c.float32()
	if math.IsNaN(v) {
		return nil
	}
	unit := "°C"
	if flags&0x01 != 0 {
		unit = "°F"
	}
	if flags&0x02 != 0 {
		if ts, ok := c.timestamp(); ok {
			at = ts
		}
	}
	return []model.Reading{{Metric: model.MetricTemperature, Value: round2(v), Unit: unit, Quality: model.QualityOK, At: at}}
}

// --- Blood Pressure (0x1810) ---------------------------------------------

func bloodPressure(c *cursor, at time.Time) []model.Reading {
	flags, ok := c.u8()
	if !ok {
		return nil
	}
	unit := "mmHg"
	if flags&0x01 != 0 {
		unit = "kPa"
	}
	sys, dia, mean := c.sfloat(), c.sfloat(), c.sfloat()
	if flags&0x02 != 0 {
		if ts, ok := c.timestamp(); ok {
			at = ts
		}
	}
	var out []model.Reading
	add := func(m model.Metric, v float64, u string) {
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			out = append(out, model.Reading{Metric: m, Value: round2(v), Unit: u, Quality: model.QualityOK, At: at})
		}
	}
	add(model.MetricBPSystolic, sys, unit)
	add(model.MetricBPDiastolic, dia, unit)
	add(model.MetricBPMean, mean, unit)
	if flags&0x04 != 0 {
		add(model.MetricPulseRate, c.sfloat(), "bpm")
	}
	return out
}

// --- Heart Rate (0x180D) --------------------------------------------------

func heartRate(c *cursor, at time.Time) []model.Reading {
	flags, ok := c.u8()
	if !ok {
		return nil
	}
	var hr float64
	if flags&0x01 != 0 {
		v, ok := c.u16()
		if !ok {
			return nil
		}
		hr = float64(v)
	} else {
		v, ok := c.u8()
		if !ok {
			return nil
		}
		hr = float64(v)
	}
	if hr == 0 {
		return nil
	}
	return []model.Reading{{Metric: model.MetricPulseRate, Value: hr, Unit: "bpm", Quality: model.QualityOK, At: at}}
}

// --- little-endian cursor -------------------------------------------------

// cursor reads little-endian fields and never panics: once it runs off the end
// every read reports not-ok, so a truncated notification degrades to fewer
// readings instead of crashing the session pump.
type cursor struct {
	b []byte
	i int
}

func (c *cursor) remaining() int { return len(c.b) - c.i }

func (c *cursor) skip(n int) { c.i += n }

func (c *cursor) take(n int) ([]byte, bool) {
	if c.remaining() < n {
		c.i = len(c.b)
		return nil, false
	}
	v := c.b[c.i : c.i+n]
	c.i += n
	return v, true
}

func (c *cursor) u8() (uint8, bool) {
	b, ok := c.take(1)
	if !ok {
		return 0, false
	}
	return b[0], true
}

func (c *cursor) u16() (uint16, bool) {
	b, ok := c.take(2)
	if !ok {
		return 0, false
	}
	return uint16(b[0]) | uint16(b[1])<<8, true
}

func (c *cursor) sfloat() float64 {
	b, ok := c.take(2)
	if !ok {
		return math.NaN()
	}
	return sfloat16(b)
}

func (c *cursor) float32() float64 {
	b, ok := c.take(4)
	if !ok {
		return math.NaN()
	}
	return float32med(b)
}

// timestamp reads the 7-byte SIG date-time. Many devices ship with an unset
// clock, so a year outside a sane range is reported as absent and the caller
// keeps the host arrival time instead.
func (c *cursor) timestamp() (time.Time, bool) {
	b, ok := c.take(7)
	if !ok {
		return time.Time{}, false
	}
	year := int(uint16(b[0]) | uint16(b[1])<<8)
	if year < 2000 || year > 2200 || b[2] < 1 || b[2] > 12 || b[3] < 1 || b[3] > 31 {
		return time.Time{}, false
	}
	return time.Date(year, time.Month(b[2]), int(b[3]), int(b[4]), int(b[5]), int(b[6]), 0, time.Local), true
}
