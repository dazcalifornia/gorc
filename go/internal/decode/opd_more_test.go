package decode

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"github.com/expert/go-connector/internal/model"
	"github.com/expert/go-connector/internal/transport"
)

func gatt(short string, b ...byte) transport.Frame {
	return transport.Frame{At: at, Source: "0000" + short + "-0000-1000-8000-00805f9b34fb", Data: b}
}

// --- Xiaomi Mi scale ------------------------------------------------------

func miFrame(kg float64, stable bool) []byte {
	b := make([]byte, 13)
	if stable {
		b[1] = 0x20
	}
	binary.LittleEndian.PutUint16(b[11:], uint16(kg*200))
	return b
}

func TestMiScaleWeightAtBytes11And12(t *testing.T) {
	rs := newDecoder(t, "scale-mi-ble").Feed(gatt("2a9c", miFrame(68.4, true)...))
	r := pick(t, rs, model.MetricWeight)
	near(t, r.Value, 68.4)
	if r.Quality != model.QualityOK {
		t.Fatalf("stable weight quality = %s", r.Quality)
	}
}

func TestMiScaleSettlingWeightIsArtifactAndRepeatsAreDropped(t *testing.T) {
	d := newDecoder(t, "scale-mi-ble")
	if r := pick(t, d.Feed(gatt("2a9c", miFrame(68.0, false)...)), model.MetricWeight); r.Quality != model.QualityArtifact {
		t.Fatalf("settling weight quality = %s", r.Quality)
	}
	if rs := d.Feed(gatt("2a9c", miFrame(68.0, false)...)); len(rs) != 0 {
		t.Fatalf("repeat produced %v", rs)
	}
	if rs := d.Feed(gatt("2a9c", miFrame(68.0, true)...)); len(rs) == 0 {
		t.Fatal("the weight settling should report")
	}
}

// --- HTD thermometer gun --------------------------------------------------

func TestHTDAsciiAfterFourCharPrefix(t *testing.T) {
	rs := newDecoder(t, "thermo-htd-ble").Feed(gatt("ffe1", []byte("T:B 36.7C")...))
	near(t, pick(t, rs, model.MetricTemperature).Value, 36.7)
}

func TestHTDDropsUnparseableAndOtherCharacteristics(t *testing.T) {
	d := newDecoder(t, "thermo-htd-ble")
	for _, f := range []transport.Frame{
		gatt("ffe1", []byte("ERR:LO")...),
		gatt("ffe4", []byte("T:B 36.7C")...),
	} {
		if rs := d.Feed(f); len(rs) != 0 {
			t.Fatalf("%q produced %v", f.Data, rs)
		}
	}
}

// --- glucose --------------------------------------------------------------

func glucoseRecord(flags byte, conc []byte) []byte {
	b := []byte{flags, 0x07, 0x00, 0xEA, 0x07, 11, 26, 8, 30, 0} // seq 7, 2026-11-26 08:30:00
	if flags&0x01 != 0 {
		b = append(b, 0x00, 0x00) // time offset 0
	}
	if conc != nil {
		b = append(b, conc...)
		b = append(b, 0x11)
	}
	return b
}

func sfloat(mantissa int16, exp int8) []byte {
	raw := uint16(exp&0x0F)<<12 | uint16(mantissa)&0x0FFF
	return []byte{byte(raw), byte(raw >> 8)}
}

func TestGlucoseKgPerLitreToMgDl(t *testing.T) {
	// The OPD read byte 12 here: right for 112, wrong for anything over 255.
	rs := newDecoder(t, "ble-glucose").Feed(gatt("2a18", glucoseRecord(0x03, sfloat(112, -5))...))
	r := pick(t, rs, model.MetricGlucose)
	near(t, r.Value, 112)
	if r.Unit != "mg/dL" {
		t.Fatalf("unit = %q", r.Unit)
	}
	if want := time.Date(2026, 11, 26, 8, 30, 0, 0, time.Local); !r.At.Equal(want) && !r.At.Equal(at) {
		t.Fatalf("at = %v", r.At)
	}

	rs = newDecoder(t, "ble-glucose").Feed(gatt("2a18", glucoseRecord(0x03, sfloat(312, -5))...))
	near(t, pick(t, rs, model.MetricGlucose).Value, 312)
}

func TestGlucoseMolPerLitreStaysMmol(t *testing.T) {
	rs := newDecoder(t, "ble-glucose").Feed(gatt("2a18", glucoseRecord(0x06, sfloat(62, -4))...))
	r := pick(t, rs, model.MetricGlucose)
	near(t, r.Value, 6.2)
	if r.Unit != "mmol/L" {
		t.Fatalf("unit = %q", r.Unit)
	}
}

func TestGlucoseRecordWithoutConcentrationIsSkipped(t *testing.T) {
	if rs := newDecoder(t, "ble-glucose").Feed(gatt("2a18", glucoseRecord(0x00, nil)...)); len(rs) != 0 {
		t.Fatalf("got %v", rs)
	}
}

func TestGlucoseProfileAsksForLastRecord(t *testing.T) {
	gp, ok := newDecoder(t, "ble-glucose").(GATTPrimer)
	if !ok {
		t.Fatal("ble-glucose should write to the RACP")
	}
	w := gp.OnOpenGATT()
	if len(w) != 1 || !strings.Contains(w[0].Char, "2a52") || string(w[0].Data) != "\x01\x06" {
		t.Fatalf("writes = %+v", w)
	}
}

// --- HL7 OBX --------------------------------------------------------------

func hl7(segs ...string) transport.Frame {
	return transport.Frame{At: at, Source: "tcp:10.0.0.5:4000", Data: []byte("\x0b" + strings.Join(segs, "\r") + "\r\x1c\r")}
}

func value(t *testing.T, rs []model.Reading, m model.Metric) float64 {
	t.Helper()
	return pick(t, rs, m).Value
}

func TestHL7MDCMonitorExample(t *testing.T) {
	// docs/Core/04-HL7-Monitor.md §5.5 example 1.
	rs := newDecoder(t, "hl7-obx").Feed(hl7(
		`MSH|^~\&|MON|ICU|||20261126083000||ORU^R01|42|P|2.6`,
		"PID|1||HN001",
		"OBX|1|NM|150456^MDC_PULS_OXIM_SAT_O2^MDC||98|%",
		"OBX|2|NM|149530^MDC_PULS_OXIM_PULS_RATE^MDC||72|bpm",
		"OBX|3|NM|147842^MDC_ECG_HEART_RATE^MDC||75|bpm",
		"OBX|4|NM|150021^MDC_PRESS_BLD_NONINV_SYS^MDC||120|mmHg",
		"OBX|5|NM|150022^MDC_PRESS_BLD_NONINV_DIA^MDC||80|mmHg",
		"OBX|6|NM|150023^MDC_PRESS_BLD_NONINV_MEAN^MDC||93|mmHg",
		"OBX|7|NM|149546^MDC_PULS_RATE_NON_INV^MDC||74|bpm",
		"OBX|8|NM|150344^MDC_TEMP^MDC|1.2.1.150344|36.5|cel",
	))
	near(t, value(t, rs, model.MetricSpO2), 98)
	near(t, value(t, rs, model.MetricHeartRate), 75)
	near(t, value(t, rs, model.MetricPulseRate), 74) // NIBP pulse after SpO2 pulse wins
	near(t, value(t, rs, model.MetricBPSystolic), 120)
	near(t, value(t, rs, model.MetricBPDiastolic), 80)
	near(t, value(t, rs, model.MetricBPMean), 93)
	near(t, value(t, rs, model.MetricTemperature), 36.5)
}

func TestHL7ShortCodeMonitorExample(t *testing.T) {
	// docs/Core/04-HL7-Monitor.md §5.5 example 2 (IM70-style).
	rs := newDecoder(t, "hl7-obx").Feed(hl7(
		"MSH|^~\\&|IM70",
		"OBX|1|NM|NIBP_SYS||118|",
		"OBX|2|NM|NIBP_DIA||76|",
		"OBX|3|NM|NIBP_MAP||90|",
		"OBX|4|NM|NIBP_PR||70|",
		"OBX|5|NM|SpO2||97|",
		"OBX|6|NM|RR||18|",
		"OBX|7|NM|T1||36.7|",
	))
	near(t, value(t, rs, model.MetricBPSystolic), 118)
	near(t, value(t, rs, model.MetricPulseRate), 70)
	near(t, value(t, rs, model.MetricSpO2), 97)
	near(t, value(t, rs, model.MetricRespRate), 18)
	near(t, value(t, rs, model.MetricTemperature), 36.7)
	if has(rs, model.MetricHeartRate) {
		t.Fatal("no HR was sent")
	}
}

func TestHL7TemperatureInTenthsAndJunkValues(t *testing.T) {
	rs := newDecoder(t, "hl7-obx").Feed(hl7(
		"OBX|1|NM|TEMP||365|",
		"OBX|2|NM|SPO2||0|",       // finger out
		"OBX|3|NM|SYS||-|",        // no value
		"OBX|4|NM|UNKNOWN_X||55|", // not ours
		"OBX|5|NM|HR",             // too few fields
	))
	if len(rs) != 1 {
		t.Fatalf("got %v", rs)
	}
	near(t, value(t, rs, model.MetricTemperature), 36.5)
}

// --- USB-HID thermometer 0C45:7702 ----------------------------------------

func hidReport(s string) transport.Frame {
	b := make([]byte, 9)
	copy(b[1:], s) // byte 0 is the report ID
	return transport.Frame{At: at, Source: "hid", Data: b}
}

func TestHIDThermoTwoReportReading(t *testing.T) {
	d := newDecoder(t, "thermo-hid-0c45")
	if rs := d.Feed(hidReport("^00136")); len(rs) != 0 {
		t.Fatalf("degrees alone produced %v", rs)
	}
	rs := d.Feed(hidReport("5C"))
	near(t, pick(t, rs, model.MetricTemperature).Value, 36.5)

	// The tenths report completes one reading only.
	if rs := d.Feed(hidReport("5C")); len(rs) != 0 {
		t.Fatalf("second tenths report produced %v", rs)
	}
}

func TestHIDThermoOtherCaretFormUsesLastTwoDigits(t *testing.T) {
	d := newDecoder(t, "thermo-hid-0c45")
	d.Feed(hidReport("^2Tb37"))
	near(t, pick(t, d.Feed(hidReport("2C")), model.MetricTemperature).Value, 37.2)
}

func TestHIDThermoNoMeasurementAndOutOfRange(t *testing.T) {
	d := newDecoder(t, "thermo-hid-0c45")
	d.Feed(hidReport("^000Tb"))
	if rs := d.Feed(hidReport("0C")); len(rs) != 0 {
		t.Fatalf("^000Tb then C produced %v", rs)
	}
	d.Feed(hidReport("^00199"))
	if rs := d.Feed(hidReport("9C")); len(rs) != 0 {
		t.Fatalf("99.9 °C produced %v", rs)
	}
}

func TestHIDThermoPollsWithEmptyReport(t *testing.T) {
	p := newDecoder(t, "thermo-hid-0c45").(Primer)
	payload, every := p.Keepalive()
	if every != 500*time.Millisecond || len(payload) != 1 || payload[0] != 0 {
		t.Fatalf("keepalive % X every %v", payload, every)
	}
}
