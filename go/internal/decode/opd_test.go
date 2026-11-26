package decode

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"github.com/expert/go-connector/internal/model"
	"github.com/expert/go-connector/internal/transport"
)

// Vectors come from the SMART EASY OPD code comments where it had one
// (docs/Core/01-Serial-COM-HID.md); the rest are built from the documented
// layouts, since the OPD code carries no sample for those models.

var at = time.Unix(1762500000, 0)

func text(s string) transport.Frame { return frame("rx", []byte(s)...) }

func newDecoder(t *testing.T, id string) Decoder {
	t.Helper()
	d, err := New(id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// feedIdle feeds the chunks then signals the idle gap, as the pump does.
func feedIdle(t *testing.T, d Decoder, chunks ...transport.Frame) []model.Reading {
	t.Helper()
	var out []model.Reading
	for _, c := range chunks {
		out = append(out, d.Feed(c)...)
	}
	if idler, ok := d.(IdleFlusher); ok {
		out = append(out, idler.Idle(at)...)
	}
	return out
}

func wantBP(t *testing.T, rs []model.Reading, sys, dia, pulse float64) {
	t.Helper()
	near(t, pick(t, rs, model.MetricBPSystolic).Value, sys)
	near(t, pick(t, rs, model.MetricBPDiastolic).Value, dia)
	near(t, pick(t, rs, model.MetricPulseRate).Value, pulse)
	if u := pick(t, rs, model.MetricBPSystolic).Unit; u != "mmHg" {
		t.Fatalf("unit = %q", u)
	}
}

// --- CSV family -----------------------------------------------------------

func TestOmronHBP9010CompletesOnLineFeed(t *testing.T) {
	d := newDecoder(t, "bp-omron-hbp9010")
	// Sample from frmEasyOPD_BP1280.Serial.vb, split mid-field.
	rs := d.Feed(text("2026,08,24,14,10,                    ,0,11"))
	if len(rs) != 0 {
		t.Fatalf("partial line produced %v", rs)
	}
	rs = d.Feed(text("3, 71, 91,1\r\n"))
	wantBP(t, rs, 113, 71, 91)
}

func TestOmron9030ParsesOnIdle(t *testing.T) {
	d := newDecoder(t, "bp-omron-9030")
	rs := feedIdle(t, d, text("2021,05,25,09,42, ,0,114, 79,114,0"))
	wantBP(t, rs, 114, 79, 114)
}

func TestOmron9020UsesFields467AndOddParity(t *testing.T) {
	rs := feedIdle(t, newDecoder(t, "bp-omron-9020"), text("x,x,x,x,128,x, 84, 66\r\n"))
	wantBP(t, rs, 128, 84, 66)

	p, _ := Lookup("bp-omron-9020")
	if p.Line == nil || p.Line.Parity != "odd" || p.DefaultBaud != 2400 {
		t.Fatalf("profile line = %+v baud %d", p.Line, p.DefaultBaud)
	}
}

func TestCSVBurstKeepsLastValidRecord(t *testing.T) {
	rs := feedIdle(t, newDecoder(t, "bp-accuniq"),
		text("a,b,c,d,110,x,70,60\r\na,b,c,d,131,x,85,77\r\ngarbage\r\n"))
	wantBP(t, rs, 131, 85, 77)
}

func TestTerumoBP500PollsReadThenClear(t *testing.T) {
	p, ok := newDecoder(t, "bp-terumo-bp500").(Primer)
	if !ok {
		t.Fatal("terumo decoder should prime the device")
	}
	payload, every := p.Keepalive()
	if every != 5*time.Second || string(payload) != "\x02\x05\x03\x0a\x02\x06\x03\x0b" {
		t.Fatalf("keepalive = % X every %v", payload, every)
	}
	if prof, _ := Lookup("bp-terumo-bp500"); prof.Line == nil || prof.Line.StopBits != 2 {
		t.Fatalf("terumo should be 8N2, got %+v", prof.Line)
	}
}

func TestBPOutOfRangeIsDropped(t *testing.T) {
	// Fields that parse but are not a blood pressure: SYS below 40, and
	// diastolic above systolic.
	for _, line := range []string{"x,x,x,x,30,x,20,60", "x,x,x,x,90,x,120,60"} {
		if rs := feedIdle(t, newDecoder(t, "bp-accuniq"), text(line)); len(rs) != 0 {
			t.Fatalf("%q produced %v", line, rs)
		}
	}
}

func TestBPKeepsPressureWhenPulseInvalid(t *testing.T) {
	rs := feedIdle(t, newDecoder(t, "bp-accuniq"), text("x,x,x,x,120,x,80,0"))
	near(t, pick(t, rs, model.MetricBPSystolic).Value, 120)
	if has(rs, model.MetricPulseRate) {
		t.Fatal("pulse 0 should not be reported")
	}
}

// --- A&D / Terumo 2655 ----------------------------------------------------

func TestAnD2655RecordSeparator(t *testing.T) {
	rs := feedIdle(t, newDecoder(t, "bp-and-2655"), text("S 122\x1eD 78\x1eP 64\x1e"))
	wantBP(t, rs, 122, 78, 64)
}

func TestAnD2657pTakesLatestTM2655Record(t *testing.T) {
	rs := feedIdle(t, newDecoder(t, "bp-and-2655"),
		text("TM2655S100\x1eD60\x1eP50\x1eTM2655S133\x1eD88\x1eP71\x1e"))
	wantBP(t, rs, 133, 88, 71)
}

// --- BIO320 / BIO750 ------------------------------------------------------

func bioFrame(head string, body map[int]string) []byte {
	b := []byte(strings.Repeat(".", 64))
	copy(b[34:], head)
	for i, s := range body {
		copy(b[i:], s)
	}
	return b
}

func TestBIOResultFrame(t *testing.T) {
	b := bioFrame("", map[int]string{33: "S124", 38: "M096", 43: "D082", 48: "P070"})
	rs := feedIdle(t, newDecoder(t, "bp-bio"), frame("rx", b...))
	wantBP(t, rs, 124, 82, 70)
	near(t, pick(t, rs, model.MetricBPMean).Value, 96)
}

func TestBIOStartStopFramesAreIgnored(t *testing.T) {
	// The OPD code re-reported the previous values here (quirk Q6).
	for _, ctl := range []string{"STA", "STP"} {
		b := bioFrame(ctl, map[int]string{33: "S", 38: "M096", 43: "D082", 48: "P070"})
		if rs := feedIdle(t, newDecoder(t, "bp-bio"), frame("rx", b...)); len(rs) != 0 {
			t.Fatalf("%s produced %v", ctl, rs)
		}
	}
}

// --- CONTEC HL7 -----------------------------------------------------------

func TestContecTokenPositions(t *testing.T) {
	tok := make([]string, 150)
	tok[0] = "MSH"
	tok[104], tok[117], tok[143] = "119", "77", "68"
	msg := "\x0b" + strings.Join(tok, "|") + "\x1c\r"
	d := newDecoder(t, "bp-contec-hl7")
	rs := d.Feed(text(msg)) // MLLP end-of-block completes it without idle
	wantBP(t, rs, 119, 77, 68)
}

// --- Hingmed --------------------------------------------------------------

func TestHingmedTailOffsets(t *testing.T) {
	b := make([]byte, 24)
	n := len(b)
	b[n-15], b[n-13], b[n-9] = 141, 92, 80
	rs := feedIdle(t, newDecoder(t, "bp-hingmed"), frame("rx", b...))
	wantBP(t, rs, 141, 92, 80)
}

// --- Jawon ----------------------------------------------------------------

func TestJawonFrameAcrossChunksWithGarbage(t *testing.T) {
	f := []byte(strings.Repeat("0", jawonFrameLen))
	f[0] = 0x02
	copy(f[1:], "R1")
	copy(f[28:], "126")
	copy(f[32:], "098")
	copy(f[36:], "084")
	copy(f[40:], "073")
	d := newDecoder(t, "bp-jawon")
	stream := append([]byte("junk"), f...)
	rs := d.Feed(frame("rx", stream[:30]...))
	rs = append(rs, d.Feed(frame("rx", stream[30:]...))...)
	wantBP(t, rs, 126, 84, 73)
	near(t, pick(t, rs, model.MetricBPMean).Value, 98)
}

// --- Kenz -----------------------------------------------------------------

func TestKenzSTXETX(t *testing.T) {
	rs := newDecoder(t, "bp-kenz").Feed(text("\x02 AB 123456789 118 076 065\x03"))
	wantBP(t, rs, 118, 76, 65)
	if p, _ := Lookup("bp-kenz"); p.Line == nil || p.Line.StopBits != 2 || p.DefaultBaud != 1200 {
		t.Fatalf("kenz should be 1200 8N2, got %+v %d", p.Line, p.DefaultBaud)
	}
}

// --- Raycome RBP-7000 -----------------------------------------------------

func TestRaycomeBinaryWithSplitHeader(t *testing.T) {
	f := make([]byte, raycomeFrameLen)
	copy(f, raycomeHeader)
	binary.LittleEndian.PutUint16(f[14:], 138)
	binary.LittleEndian.PutUint16(f[16:], 86)
	f[18] = 79
	stream := append([]byte{0x01, 0x02}, f...)
	d := newDecoder(t, "bp-raycome")
	rs := d.Feed(frame("rx", stream[:4]...)) // header split across reads
	rs = append(rs, d.Feed(frame("rx", stream[4:]...))...)
	wantBP(t, rs, 138, 86, 79)
}

// --- SY-G100 --------------------------------------------------------------

func TestSYG100Packet(t *testing.T) {
	d := newDecoder(t, "bp-sy-g100")
	rs := d.Feed(text("noise55AA,01,02,1"))
	rs = append(rs, d.Feed(text("21,81,69,\r\n"))...)
	wantBP(t, rs, 121, 81, 69)
}

// --- Yuwell ---------------------------------------------------------------

func TestYuwellLabelledSample(t *testing.T) {
	// Sample from frmEasyOPD_BP1280.Serial.vb.
	s := "2022/11/30/15:49:8;Name:;ID:;SYS:102mmHg;MAP:77mmHg;DIA:65mmHg;PR:62bpm;SYS_Level??Normal;DIA_Level??Normal;"
	rs := feedIdle(t, newDecoder(t, "bp-yuwell"), text(s))
	wantBP(t, rs, 102, 65, 62)
	near(t, pick(t, rs, model.MetricBPMean).Value, 77)
}

// --- thermometer ----------------------------------------------------------

func TestThermoBinaryAndText(t *testing.T) {
	d := newDecoder(t, "thermo-serial")
	rs := d.Feed(frame("rx", 0xFE, 0xFD, 0x1A, 0x01, 0x01, 0x6D, 0x0D, 0x0A))
	near(t, pick(t, rs, model.MetricTemperature).Value, 36.5)

	rs = d.Feed(text("T body = 36.293 C, weak low\r\n"))
	near(t, pick(t, rs, model.MetricTemperature).Value, 36.3)
}

func TestThermoRejectsOutOfRangeBinary(t *testing.T) {
	// " C\r\n" read as binary would be 825.9 °C.
	if rs := newDecoder(t, "thermo-serial").Feed(text("weak C\r\n")); len(rs) != 0 {
		t.Fatalf("got %v", rs)
	}
}

// --- kscm01 ---------------------------------------------------------------

func TestKscm01MeasurementAndPleth(t *testing.T) {
	d := newDecoder(t, "spo2-kscm01")
	if p, ok := d.(Primer); !ok || string(p.OnOpen()[0]) != string(kscm01Start) {
		t.Fatal("kscm01 should send its start command on open")
	}
	// Samples from LIB/SpO2Lib.vb, split mid-frame with garbage in front.
	stream := []byte{0x00, 0xAA, 0x55, 0x53, 0x07, 0x01, 0x63, 0x39, 0x00, 0x13, 0x00, 0x6E,
		0xAA, 0x55, 0x52, 0x03, 0x01, 0x00, 0x23}
	rs := d.Feed(frame("rx", stream[:7]...))
	rs = append(rs, d.Feed(frame("rx", stream[7:]...))...)

	near(t, pick(t, rs, model.MetricSpO2).Value, 99)
	near(t, pick(t, rs, model.MetricPulseRate).Value, 57)
	near(t, pick(t, rs, model.MetricPerfusionIndex).Value, 1.9)
	if !pick(t, rs, model.MetricPleth).Waveform {
		t.Fatal("0x52 should be a waveform sample")
	}
	// 0x52 must never surface as SpO2 (OPD quirk Q12).
	n := 0
	for _, r := range rs {
		if r.Metric == model.MetricSpO2 {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("got %d SpO2 readings, want 1", n)
	}
}

// --- OPD scale formats through ascii-lines --------------------------------

func TestASCIICompactWHT(t *testing.T) {
	rs := newDecoder(t, "ascii-lines").Feed(text("W:067.9H:180.0T:36.5\r\n"))
	near(t, pick(t, rs, model.MetricWeight).Value, 67.9)
	near(t, pick(t, rs, model.MetricHeight).Value, 180)
	near(t, pick(t, rs, model.MetricTemperature).Value, 36.5)
}

func TestASCIISCStandSkipsZeroHeight(t *testing.T) {
	rs := newDecoder(t, "ascii-lines").Feed(text("[ST,N,W+000062.5kg,H000000.0cm,B000000.00]\r\n"))
	near(t, pick(t, rs, model.MetricWeight).Value, 62.5)
	if has(rs, model.MetricHeight) {
		t.Fatal("a zero height column is not a measurement")
	}
}

func TestASCIIAdamRecordIgnoresDateTimeTag(t *testing.T) {
	// Sample from frmEasyOPD_SC600.vb; records are split by control chars.
	rec := "DoctorID: 00000\r\nPatientID: 0000000000\r\nWeight: 58.20 Kg\r\nHeight: 166 cm\r\nBMI: 21.1\r\nD&T: 17/07/2026 13.34\r\n"
	rs := newDecoder(t, "ascii-lines").Feed(text(rec))
	near(t, pick(t, rs, model.MetricWeight).Value, 58.2)
	near(t, pick(t, rs, model.MetricHeight).Value, 166)
	if has(rs, model.MetricTemperature) {
		t.Fatal("'D&T: 17' is a date, not a temperature")
	}
}

func TestASCIITanitaWk(t *testing.T) {
	rs := newDecoder(t, "ascii-lines").Feed(text("{0,16,~0,1,Wk,62.5,Hc,0}\r\n"))
	near(t, pick(t, rs, model.MetricWeight).Value, 62.5)
}

// The A&D form still wins when both could apply.
func TestASCIILongLabelsBeatCompactTags(t *testing.T) {
	rs := newDecoder(t, "ascii-lines").Feed(text("WT:70.5 HT:170\r\n"))
	near(t, pick(t, rs, model.MetricHeight).Value, 170)
	if has(rs, model.MetricTemperature) {
		t.Fatal("HT: must not read as a T: temperature")
	}
}
