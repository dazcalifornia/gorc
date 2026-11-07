package decode

import (
	"math"
	"testing"
	"time"

	"github.com/expert/go-connector/internal/btsig"
	"github.com/expert/go-connector/internal/model"
	"github.com/expert/go-connector/internal/transport"
)

func frame(src string, b ...byte) transport.Frame {
	return transport.Frame{At: time.Unix(1762500000, 0), Source: src, Data: b}
}

// pick returns the value of the first reading with the given metric.
func pick(t *testing.T, rs []model.Reading, m model.Metric) model.Reading {
	t.Helper()
	for _, r := range rs {
		if r.Metric == m {
			return r
		}
	}
	t.Fatalf("no %s reading in %+v", m, rs)
	return model.Reading{}
}

func has(rs []model.Reading, m model.Metric) bool {
	for _, r := range rs {
		if r.Metric == m {
			return true
		}
	}
	return false
}

func near(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.005 {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// --- BCI pulse oximeter ---------------------------------------------------

// bciFrame builds a 5-byte BCI frame the way the device would.
func bciFrame(signal, pleth, pulse, spo2 int, searching, noSensor bool) []byte {
	b0 := byte(0x80 | signal&0x0F)
	if searching {
		b0 |= 0x10
	}
	if noSensor {
		b0 |= 0x20
	}
	b2 := byte(0x0A)
	if pulse&0x80 != 0 {
		b2 |= 0x40
	}
	return []byte{b0, byte(pleth & 0x7F), b2, byte(pulse & 0x7F), byte(spo2 & 0x7F)}
}

func TestBCIDecodesSpO2PulseAndPleth(t *testing.T) {
	d := &bciSpO2{}
	rs := d.Feed(frame("rx", bciFrame(8, 50, 72, 98, false, false)...))

	near(t, pick(t, rs, model.MetricSpO2).Value, 98)
	near(t, pick(t, rs, model.MetricPulseRate).Value, 72)
	near(t, pick(t, rs, model.MetricPleth).Value, 50)
	near(t, pick(t, rs, model.MetricSignalStrength).Value, 8)

	if u := pick(t, rs, model.MetricSpO2).Unit; u != "%" {
		t.Fatalf("spo2 unit = %q", u)
	}
	if !pick(t, rs, model.MetricPleth).Waveform {
		t.Fatal("pleth should be flagged as waveform")
	}
}

// A pulse rate above 127 needs the high bit carried in byte 2, which is the
// easiest part of this protocol to get wrong.
func TestBCIPulseRateAbove127(t *testing.T) {
	d := &bciSpO2{}
	rs := d.Feed(frame("rx", bciFrame(8, 10, 200, 95, false, false)...))
	near(t, pick(t, rs, model.MetricPulseRate).Value, 200)
}

func TestBCIResyncsAfterGarbage(t *testing.T) {
	d := &bciSpO2{}
	// Leading junk plus a truncated frame, then a good one.
	in := append([]byte{0x01, 0x02, 0x03}, bciFrame(7, 20, 65, 97, false, false)...)
	rs := d.Feed(frame("rx", in...))
	near(t, pick(t, rs, model.MetricSpO2).Value, 97)
	near(t, pick(t, rs, model.MetricPulseRate).Value, 65)
}

// Bytes arriving split across reads must not produce a scrambled reading.
func TestBCIHandlesSplitFrames(t *testing.T) {
	d := &bciSpO2{}
	f := bciFrame(8, 33, 88, 99, false, false)
	if rs := d.Feed(frame("rx", f[:3]...)); len(rs) != 0 {
		t.Fatalf("partial frame produced %d readings", len(rs))
	}
	rs := d.Feed(frame("rx", f[3:]...))
	near(t, pick(t, rs, model.MetricSpO2).Value, 99)
	near(t, pick(t, rs, model.MetricPulseRate).Value, 88)
}

func TestBCIReportsSearchingAndNoSensor(t *testing.T) {
	d := &bciSpO2{}
	rs := d.Feed(frame("rx", bciFrame(0, 0, 0, 127, true, false)...))
	if q := pick(t, rs, model.MetricPleth).Quality; q != model.QualitySearching {
		t.Fatalf("quality = %q, want searching", q)
	}
	d2 := &bciSpO2{}
	rs = d2.Feed(frame("rx", bciFrame(0, 0, 0, 127, false, true)...))
	if q := pick(t, rs, model.MetricPleth).Quality; q != model.QualityNoSensor {
		t.Fatalf("quality = %q, want no_sensor", q)
	}
	// SpO2 of 127 is the protocol's "invalid" sentinel and must not surface.
	if has(rs, model.MetricSpO2) {
		t.Fatal("invalid SpO2 sentinel 127 was emitted as a reading")
	}
}

// Unchanged scalars are suppressed between heartbeats, but the waveform keeps
// flowing — otherwise the UI chart stalls.
func TestBCISuppressesUnchangedScalars(t *testing.T) {
	d := &bciSpO2{}
	f := bciFrame(8, 40, 70, 97, false, false)
	d.Feed(frame("rx", f...))
	rs := d.Feed(transport.Frame{At: time.Unix(1762500000, 0), Source: "rx", Data: f})
	if has(rs, model.MetricSpO2) {
		t.Fatalf("unchanged SpO2 re-emitted: %+v", rs)
	}
	if !has(rs, model.MetricPleth) {
		t.Fatal("waveform sample was suppressed")
	}
}

// --- ASCII line devices ---------------------------------------------------

func TestASCIIAndDStyleWeight(t *testing.T) {
	d := &asciiLines{last: map[model.Metric]float64{}}
	rs := d.Feed(frame("rx", []byte("ST,GS,+   70.55 kg\r\n")...))
	w := pick(t, rs, model.MetricWeight)
	near(t, w.Value, 70.55)
	if w.Unit != "kg" || w.Quality != model.QualityOK {
		t.Fatalf("got %+v", w)
	}
}

func TestASCIIUnstableIsArtifact(t *testing.T) {
	d := &asciiLines{last: map[model.Metric]float64{}}
	rs := d.Feed(frame("rx", []byte("US,GS,+   69.80 kg\r\n")...))
	if q := pick(t, rs, model.MetricWeight).Quality; q != model.QualityArtifact {
		t.Fatalf("quality = %q, want artifact", q)
	}
}

func TestASCIIStadiometerHeight(t *testing.T) {
	d := &asciiLines{last: map[model.Metric]float64{}}
	rs := d.Feed(frame("rx", []byte("   170.5 cm\r\n")...))
	h := pick(t, rs, model.MetricHeight)
	near(t, h.Value, 170.5)
	if h.Unit != "cm" {
		t.Fatalf("unit = %q", h.Unit)
	}
}

func TestASCIILabelledFields(t *testing.T) {
	d := &asciiLines{last: map[model.Metric]float64{}}
	rs := d.Feed(frame("rx", []byte("WT:70.5 HT:170.2 BMI:24.4\r\n")...))
	near(t, pick(t, rs, model.MetricWeight).Value, 70.5)
	near(t, pick(t, rs, model.MetricHeight).Value, 170.2)
	near(t, pick(t, rs, model.MetricBMI).Value, 24.4)
}

func TestASCIIGramsNormaliseToKilograms(t *testing.T) {
	d := &asciiLines{last: map[model.Metric]float64{}}
	rs := d.Feed(frame("rx", []byte("ST,NT,+  3450 g\r\n")...))
	w := pick(t, rs, model.MetricWeight)
	near(t, w.Value, 3.45)
	if w.Unit != "kg" {
		t.Fatalf("unit = %q, want kg", w.Unit)
	}
}

func TestASCIIWaitsForLineTerminator(t *testing.T) {
	d := &asciiLines{last: map[model.Metric]float64{}}
	if rs := d.Feed(frame("rx", []byte("ST,GS,+   70.5")...)); len(rs) != 0 {
		t.Fatalf("emitted %d readings before terminator", len(rs))
	}
	rs := d.Feed(frame("rx", []byte("5 kg\r\n")...))
	near(t, pick(t, rs, model.MetricWeight).Value, 70.55)
}

// --- BLE GATT -------------------------------------------------------------

const (
	uuidPLXCont = "00002a5f-0000-1000-8000-00805f9b34fb"
	uuidWeight  = "00002a9d-0000-1000-8000-00805f9b34fb"
	uuidTemp    = "00002a1c-0000-1000-8000-00805f9b34fb"
	uuidBP      = "00002a35-0000-1000-8000-00805f9b34fb"
	uuidBattery = "00002a19-0000-1000-8000-00805f9b34fb"
)

func TestShortUUID(t *testing.T) {
	if v, ok := btsig.ShortUUID(uuidPLXCont); !ok || v != 0x2A5F {
		t.Fatalf("got %#x ok=%v", v, ok)
	}
	if v, ok := btsig.ShortUUID("2a9d"); !ok || v != 0x2A9D {
		t.Fatalf("short form: got %#x ok=%v", v, ok)
	}
	if !btsig.IsVendor("6e400001-b5a3-f393-e0a9-e50e24dcca9e") {
		t.Fatal("vendor UUID should not resolve to an assigned number")
	}
}

func TestSFloat16(t *testing.T) {
	near(t, sfloat16([]byte{0x61, 0x00}), 97)      // mantissa 97, exp 0
	near(t, sfloat16([]byte{0xE4, 0xF0}), 22.8)    // mantissa 228, exp -1
	if !math.IsNaN(sfloat16([]byte{0xFF, 0x07})) { // reserved NaN pattern
		t.Fatal("0x07FF should decode as NaN")
	}
	near(t, sfloat16([]byte{0xFF, 0x0F}), -1) // mantissa -1, exp 0
}

func TestPLXContinuous(t *testing.T) {
	d := &bleGATT{}
	// flags 0, SpO2 = 97, PR = 68
	rs := d.Feed(frame(uuidPLXCont, 0x00, 0x61, 0x00, 0x44, 0x00))
	near(t, pick(t, rs, model.MetricSpO2).Value, 97)
	near(t, pick(t, rs, model.MetricPulseRate).Value, 68)
}

// Exercises the flag-driven field skipping: fast and slow SpO2PR fields sit
// between the normal field and the perfusion index.
func TestPLXContinuousWithOptionalFields(t *testing.T) {
	d := &bleGATT{}
	rs := d.Feed(frame(uuidPLXCont,
		0x01|0x02|0x10,         // fast + slow + perfusion index present
		0x61, 0x00, 0x44, 0x00, // normal: 97 / 68
		0x62, 0x00, 0x45, 0x00, // fast   (skipped)
		0x60, 0x00, 0x43, 0x00, // slow   (skipped)
		0x1E, 0xF0, // perfusion index: mantissa 30, exp -1 = 3.0
	))
	near(t, pick(t, rs, model.MetricSpO2).Value, 97)
	near(t, pick(t, rs, model.MetricPerfusionIndex).Value, 3.0)
}

func TestWeightMeasurementSIWithBMIAndHeight(t *testing.T) {
	d := &bleGATT{}
	// flags bit3 = BMI+height present; weight 14110*0.005 = 70.55 kg
	rs := d.Feed(frame(uuidWeight, 0x08, 0x1E, 0x37, 0xF4, 0x00, 0xA9, 0x06))
	near(t, pick(t, rs, model.MetricWeight).Value, 70.55)
	near(t, pick(t, rs, model.MetricBMI).Value, 24.4)
	near(t, pick(t, rs, model.MetricHeight).Value, 170.5)
}

func TestWeightMeasurementImperial(t *testing.T) {
	d := &bleGATT{}
	// flags bit0 = imperial; 15551*0.01 = 155.51 lb
	rs := d.Feed(frame(uuidWeight, 0x01, 0xBF, 0x3C))
	w := pick(t, rs, model.MetricWeight)
	near(t, w.Value, 155.51)
	if w.Unit != "lb" {
		t.Fatalf("unit = %q, want lb", w.Unit)
	}
}

func TestTemperatureMeasurement(t *testing.T) {
	d := &bleGATT{}
	// flags 0 = Celsius; FLOAT32 mantissa 3684, exponent -2 = 36.84
	rs := d.Feed(frame(uuidTemp, 0x00, 0x64, 0x0E, 0x00, 0xFE))
	r := pick(t, rs, model.MetricTemperature)
	near(t, r.Value, 36.84)
	if r.Unit != "°C" {
		t.Fatalf("unit = %q", r.Unit)
	}
}

func TestBloodPressureWithPulse(t *testing.T) {
	d := &bleGATT{}
	rs := d.Feed(frame(uuidBP, 0x04,
		0x78, 0x00, // systolic 120
		0x50, 0x00, // diastolic 80
		0x5D, 0x00, // mean 93
		0x48, 0x00, // pulse 72
	))
	near(t, pick(t, rs, model.MetricBPSystolic).Value, 120)
	near(t, pick(t, rs, model.MetricBPDiastolic).Value, 80)
	near(t, pick(t, rs, model.MetricBPMean).Value, 93)
	near(t, pick(t, rs, model.MetricPulseRate).Value, 72)
}

func TestBatteryLevel(t *testing.T) {
	d := &bleGATT{}
	rs := d.Feed(frame(uuidBattery, 0x5A))
	near(t, pick(t, rs, model.MetricBattery).Value, 90)
}

// A truncated notification is the common real-world failure: the decoder must
// return what it could read rather than panic.
func TestTruncatedNotificationIsSafe(t *testing.T) {
	d := &bleGATT{}
	for n := 0; n < 8; n++ {
		d.Feed(frame(uuidWeight, make([]byte, n)...))
		d.Feed(frame(uuidPLXCont, make([]byte, n)...))
		d.Feed(frame(uuidBP, make([]byte, n)...))
	}
}

func TestProfilesRegistered(t *testing.T) {
	want := map[string]bool{"spo2-bci": false, "ascii-lines": false, "ble-gatt": false}
	for _, p := range Profiles() {
		if _, ok := want[p.ID]; ok {
			want[p.ID] = true
		}
		if _, err := New(p.ID); err != nil {
			t.Fatalf("New(%q): %v", p.ID, err)
		}
	}
	for id, found := range want {
		if !found {
			t.Fatalf("profile %q not registered", id)
		}
	}
}
