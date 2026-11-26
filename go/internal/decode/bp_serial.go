package decode

import (
	"bytes"
	"encoding/binary"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/expert/go-connector/internal/model"
	"github.com/expert/go-connector/internal/transport"
)

// Serial blood-pressure monitors, ported from the SMART EASY OPD kiosk
// (SMART-EASY-OPD_MASTER, docs/Core/01-Serial-COM-HID.md, sections 1.4, 2.1
// and 5.5). Field positions and port settings follow that code; where it
// relied on Thread.Sleep to "wait for the frame", these decoders use the
// session's idle gap instead, and every model gets the range check that only
// the newest VB adapter had.

var (
	serialOnly = []transport.Kind{transport.KindSerial, transport.KindBTClassic}
	line8N2    = &transport.LineSettings{StopBits: 2}
	line8O1    = &transport.LineSettings{Parity: "odd"}
)

func init() {
	csv := func(id, label, note string, baud int, line *transport.LineSettings, sys, dia, pulse int, lines bool) {
		register(Profile{ID: id, Label: label, Note: note, Transports: serialOnly, DefaultBaud: baud, Line: line},
			func() Decoder { return &csvBP{sys: sys, dia: dia, pulse: pulse, lines: lines} })
	}
	csv("bp-omron-hbp9010", "BP — Omron HBP-9010 / USB (CSV, LF)",
		"Omron HBP-9010 and Omron USB-serial monitors that print 'yyyy,MM,dd,HH,mm,<id>,0,SYS,DIA,PULSE,…' per line. Fields 7/8/9. 9600 8N1.",
		9600, nil, 7, 8, 9, true)
	csv("bp-omron-9030", "BP — Omron HBP-9030 (CSV)",
		"Omron HBP-9030: same CSV layout as HBP-9010 (fields 7/8/9) at 2400 8N1, RTS asserted.",
		2400, nil, 7, 8, 9, false)
	csv("bp-omron-9020", "BP — Omron HBP-9020 (CSV)",
		"Omron HBP-9020: CSV with SYS/DIA/PULSE in fields 4/6/7. 2400 baud, odd parity.",
		2400, line8O1, 4, 6, 7, false)
	csv("bp-accuniq", "BP — Accuniq (CSV)",
		"Accuniq BP monitors: CSV with SYS/DIA/PULSE in fields 4/6/7. 38400 8N1. No sample frame in the OPD code — verify against the device.",
		38400, nil, 4, 6, 7, false)

	register(Profile{
		ID: "bp-terumo-bp500", Label: "BP — Terumo BP500 (polled CSV)",
		Note:       "Terumo BP500 answers a 'read' command polled every 5 s (followed by 'clear'), CSV fields 4/6/7. 38400 8N2.",
		Transports: serialOnly, DefaultBaud: 38400, Line: line8N2,
	}, func() Decoder { return &terumoBP500{csvBP{sys: 4, dia: 6, pulse: 7}} })

	register(Profile{
		ID: "bp-and-2655", Label: "BP — A&D TM-2655 / Terumo 2655 (RS records)",
		Note:       "A&D TM-2655, TM-2655-RTS, TM-2657P and Terumo 2655: records separated by 0x1E, each starting S/D/P. 9600 for TM-2655; pick 2400 for the RTS variants, TM-2657P and Terumo 2655.",
		Transports: serialOnly, DefaultBaud: 9600,
	}, func() Decoder { return &andBP{} })

	register(Profile{
		ID: "bp-bio", Label: "BP — BIO320 / BIO750 (fixed offsets)",
		Note:       "BIO320/BIO750 result frame ≥64 bytes with ASCII 'S' 'M' 'D' 'P' markers at 33/38/43/48; STA/STP control frames ignored. 9600 8N1.",
		Transports: serialOnly, DefaultBaud: 9600,
	}, func() Decoder { return &bioBP{} })

	register(Profile{
		ID: "bp-contec-hl7", Label: "BP — CONTEC (HL7 over serial)",
		Note:       "CONTEC BP monitors sending an HL7 message. Values are taken from '|' token positions 104/117/143 as in the OPD code, not from parsed OBX segments — verify against the device. 115200 8N1.",
		Transports: serialOnly, DefaultBaud: 115200,
	}, func() Decoder { return &contecBP{} })

	register(Profile{
		ID: "bp-hingmed", Label: "BP — Hingmed (binary, tail offsets)",
		Note:       "Hingmed binary burst: SYS/DIA/PULSE are single bytes 15/13/9 from the end. No header or checksum is known. 19200 8N1.",
		Transports: serialOnly, DefaultBaud: 19200,
	}, func() Decoder { return &hingmedBP{} })

	register(Profile{
		ID: "bp-jawon", Label: "BP — Jawon (STX, 70-byte R1)",
		Note:       "Jawon: STX-framed 70-byte result frames tagged 'R1', ASCII SYS@28 MAP@32 DIA@36 PULSE@40. 38400 8N1, RTS. Never ran in the OPD code — verify against the device.",
		Transports: serialOnly, DefaultBaud: 38400,
	}, func() Decoder { return &jawonBP{} })

	register(Profile{
		ID: "bp-kenz", Label: "BP — Kenz (STX/ETX)",
		Note:       "Kenz BP monitors: STX/ETX frame, SYS/DIA/PULSE as 3-digit ASCII at 11/14/17 after removing spaces. 1200 8N2, RTS.",
		Transports: serialOnly, DefaultBaud: 1200, Line: line8N2,
	}, func() Decoder { return &kenzBP{} })

	register(Profile{
		ID: "bp-raycome", Label: "BP — Raycome RBP-7000 (binary)",
		Note:       "Raycome RBP-7000: 20-byte frame with header AA 80 03 0F, SYS/DIA as little-endian uint16 at 14/16, PULSE at 18. 9600 8N1.",
		Transports: serialOnly, DefaultBaud: 9600,
	}, func() Decoder { return &raycomeBP{} })

	register(Profile{
		ID: "bp-sy-g100", Label: "BP — SY-G100 (55aa CSV)",
		Note:       "SY-G100: packet starting '55aa,' with SYS/DIA/PULSE in comma fields 3/4/5. 115200 8N1.",
		Transports: serialOnly, DefaultBaud: 115200,
	}, func() Decoder { return &syg100BP{} })

	register(Profile{
		ID: "bp-yuwell", Label: "BP — Yuwell USB (labelled, ';')",
		Note:       "Yuwell USB mode C: 'SYS:102mmHg;MAP:77mmHg;DIA:65mmHg;PR:62bpm;…'. 115200 8N1, RTS.",
		Transports: serialOnly, DefaultBaud: 115200,
	}, func() Decoder { return &yuwellBP{} })
}

// ---------- shared helpers

// maxBurst caps any buffered burst, as the OPD adapter did, so a device that
// streams garbage cannot grow memory without bound.
const maxBurst = 4096

// defaultBurstGap is the quiet spell that ends a burst. The OPD code slept
// 1–2 s before reading; a measurement result arrives far faster than that, so
// a shorter gap gives the same framing with less latency.
const defaultBurstGap = 400 * time.Millisecond

// burst accumulates bytes until the link goes quiet.
type burst struct{ buf []byte }

func (b *burst) add(p []byte) {
	b.buf = append(b.buf, p...)
	if len(b.buf) > maxBurst {
		b.buf = append([]byte(nil), b.buf[len(b.buf)-maxBurst:]...)
	}
}

func (b *burst) take() []byte {
	out := b.buf
	b.buf = nil
	return out
}

func (b *burst) IdleGap() time.Duration { return defaultBurstGap }

var reNumber = regexp.MustCompile(`[-+]?\d+(?:\.\d+)?`)

// num reads the leading number in s the way VB's Val() does for these
// devices: " 79" → 79, "102mmHg" → 102. It returns 0 when there is none.
func num(s string) float64 {
	m := reNumber.FindString(s)
	if m == "" {
		return 0
	}
	v, _ := strconv.ParseFloat(m, 64)
	return v
}

// bpReadings validates a blood-pressure triple and turns it into readings.
// The ranges are the OPD adapter's (SYS 40–300, DIA 20–200, PULSE 20–250);
// a reading outside them is a misparse, not a patient, so it is dropped.
// Pulse and MAP are optional and checked on their own.
func bpReadings(at time.Time, sys, dia, pulse, mean float64) []model.Reading {
	if sys < 40 || sys > 300 || dia < 20 || dia > 200 || dia >= sys {
		return nil
	}
	r := func(m model.Metric, v float64, unit string) model.Reading {
		return model.Reading{Metric: m, Value: v, Unit: unit, Quality: model.QualityOK, At: at}
	}
	out := []model.Reading{
		r(model.MetricBPSystolic, sys, "mmHg"),
		r(model.MetricBPDiastolic, dia, "mmHg"),
	}
	if mean > dia && mean < sys {
		out = append(out, r(model.MetricBPMean, mean, "mmHg"))
	}
	if pulse >= 20 && pulse <= 250 {
		out = append(out, r(model.MetricPulseRate, pulse, "bpm"))
	}
	return out
}

// splitLines splits on CR and/or LF, dropping empty lines.
func splitLines(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == '\r' || r == '\n' })
}

// ---------- CSV family: Omron 9010/9020/9030, Accuniq, Terumo BP500

type csvBP struct {
	burst
	sys, dia, pulse int
	// lines completes a record on LF without waiting for the idle gap.
	lines bool
}

func (d *csvBP) Feed(f transport.Frame) []model.Reading {
	d.add(f.Data)
	if !d.lines {
		return nil
	}
	var out []model.Reading
	for {
		i := bytes.IndexByte(d.buf, '\n')
		if i < 0 {
			return out
		}
		line := string(d.buf[:i])
		d.buf = d.buf[i+1:]
		out = append(out, d.parse(line, f.At)...)
	}
}

// Idle parses a burst; when it holds several records only the last valid one
// counts, since that is the measurement just taken.
func (d *csvBP) Idle(at time.Time) []model.Reading {
	var last []model.Reading
	for _, line := range splitLines(string(d.take())) {
		if rs := d.parse(line, at); rs != nil {
			last = rs
		}
	}
	return last
}

func (d *csvBP) parse(line string, at time.Time) []model.Reading {
	line = strings.NewReplacer("\x00", "", "?", "", "\r", "").Replace(line)
	fields := strings.Split(line, ",")
	need := max(d.sys, d.dia, d.pulse)
	if len(fields) <= need {
		return nil
	}
	return bpReadings(at, num(fields[d.sys]), num(fields[d.dia]), num(fields[d.pulse]), 0)
}

// terumoBP500 only reports when polled: "read" then "clear", every 5 s.
type terumoBP500 struct{ csvBP }

var (
	terumoRead  = []byte{0x02, 0x05, 0x03, 0x0A}
	terumoClear = []byte{0x02, 0x06, 0x03, 0x0B}
)

func (d *terumoBP500) OnOpen() [][]byte { return [][]byte{terumoClear} }

func (d *terumoBP500) Keepalive() ([]byte, time.Duration) {
	return append(append([]byte(nil), terumoRead...), terumoClear...), 5 * time.Second
}

// ---------- A&D TM-2655 family: 0x1E-separated S/D/P records

type andBP struct{ burst }

func (d *andBP) Feed(f transport.Frame) []model.Reading { d.add(f.Data); return nil }

func (d *andBP) Idle(at time.Time) []model.Reading {
	text := string(d.take())
	// TM-2657P prefixes each record with "TM2655"; the last one is newest.
	if i := strings.LastIndex(text, "TM2655"); i >= 0 {
		text = text[i+len("TM2655"):]
	}
	var sys, dia, pulse float64
	for _, rec := range strings.Split(text, "\x1e") {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}
		v := num(strings.Map(func(r rune) rune {
			if (r >= '0' && r <= '9') || r == '.' {
				return r
			}
			return -1
		}, rec))
		switch rec[0] {
		case 'S', 's':
			sys = v
		case 'D', 'd':
			dia = v
		case 'P', 'p':
			pulse = v
		}
	}
	return bpReadings(at, sys, dia, pulse, 0)
}

// ---------- BIO320 / BIO750: fixed-offset ASCII inside a ≥64-byte frame

type bioBP struct{ burst }

func (d *bioBP) Feed(f transport.Frame) []model.Reading { d.add(f.Data); return nil }

// The device sends a frame in one go; the OPD code read it 100 ms after the
// first byte.
func (d *bioBP) IdleGap() time.Duration { return 100 * time.Millisecond }

func (d *bioBP) Idle(at time.Time) []model.Reading {
	b := d.take()
	if len(b) < 64 {
		return nil
	}
	// STA / STP are the start and stop buttons, not results.
	if s := string(b[34:37]); s == "STA" || s == "STP" {
		return nil
	}
	if b[33] != 'S' || b[38] != 'M' || b[43] != 'D' || b[48] != 'P' {
		return nil
	}
	field := func(i int) float64 { return num(string(b[i : i+3])) }
	return bpReadings(at, field(34), field(44), field(49), field(39))
}

// ---------- CONTEC: HL7 message, values at fixed '|' token positions

type contecBP struct{ burst }

func (d *contecBP) Feed(f transport.Frame) []model.Reading {
	d.add(f.Data)
	// MLLP end-of-block, if the device uses it, completes the message early.
	if i := bytes.IndexByte(d.buf, 0x1C); i >= 0 {
		msg := d.buf[:i]
		d.buf = d.buf[i+1:]
		return d.parse(msg, f.At)
	}
	return nil
}

func (d *contecBP) Idle(at time.Time) []model.Reading { return d.parse(d.take(), at) }

func (d *contecBP) parse(msg []byte, at time.Time) []model.Reading {
	text := strings.ReplaceAll(string(msg), "?", "")
	if !strings.Contains(text, "MSH") {
		return nil
	}
	tok := strings.Split(text, "|")
	if len(tok) <= 143 {
		return nil
	}
	return bpReadings(at, num(tok[104]), num(tok[117]), num(tok[143]), 0)
}

// ---------- Hingmed: single bytes counted from the end of the burst

type hingmedBP struct{ burst }

func (d *hingmedBP) Feed(f transport.Frame) []model.Reading { d.add(f.Data); return nil }

func (d *hingmedBP) Idle(at time.Time) []model.Reading {
	b := d.take()
	n := len(b)
	if n < 15 {
		return nil
	}
	return bpReadings(at, float64(b[n-15]), float64(b[n-13]), float64(b[n-9]), 0)
}

// ---------- Jawon: STX + 70-byte frame tagged R1

type jawonBP struct{ burst }

const jawonFrameLen = 70

func (d *jawonBP) Feed(f transport.Frame) []model.Reading {
	d.add(f.Data)
	var out []model.Reading
	for {
		i := bytes.IndexByte(d.buf, 0x02)
		if i < 0 {
			d.buf = d.buf[:0]
			return out
		}
		d.buf = d.buf[i:]
		if len(d.buf) < jawonFrameLen {
			return out
		}
		frame := d.buf[:jawonFrameLen]
		d.buf = d.buf[jawonFrameLen:]
		if string(frame[1:3]) != "R1" {
			continue
		}
		field := func(i int) float64 { return num(string(frame[i : i+3])) }
		out = append(out, bpReadings(f.At, field(28), field(36), field(40), field(32))...)
	}
}

// ---------- Kenz: STX … ETX, fixed positions after stripping spaces

type kenzBP struct{ burst }

func (d *kenzBP) Feed(f transport.Frame) []model.Reading {
	d.add(f.Data)
	var out []model.Reading
	for {
		i := bytes.IndexByte(d.buf, 0x03)
		if i < 0 {
			return out
		}
		frame := d.buf[:i]
		d.buf = d.buf[i+1:]
		out = append(out, d.parse(frame, f.At)...)
	}
}

func (d *kenzBP) Idle(at time.Time) []model.Reading { return d.parse(d.take(), at) }

func (d *kenzBP) parse(b []byte, at time.Time) []model.Reading {
	s := strings.NewReplacer(" ", "", "\x02", "", "\x03", "").Replace(string(b))
	if len(s) < 20 {
		return nil
	}
	return bpReadings(at, num(s[11:14]), num(s[14:17]), num(s[17:20]), 0)
}

// ---------- Raycome RBP-7000: 20-byte binary frame

type raycomeBP struct{ buf []byte }

var raycomeHeader = []byte{0xAA, 0x80, 0x03, 0x0F}

const raycomeFrameLen = 20

func (d *raycomeBP) Feed(f transport.Frame) []model.Reading {
	d.buf = append(d.buf, f.Data...)
	var out []model.Reading
	for {
		i := bytes.Index(d.buf, raycomeHeader)
		if i < 0 {
			// Keep a possible half-received header for the next frame.
			if len(d.buf) > len(raycomeHeader)-1 {
				d.buf = append([]byte(nil), d.buf[len(d.buf)-(len(raycomeHeader)-1):]...)
			}
			return out
		}
		d.buf = d.buf[i:]
		if len(d.buf) < raycomeFrameLen {
			return out
		}
		fr := d.buf[:raycomeFrameLen]
		d.buf = d.buf[raycomeFrameLen:]
		sys := float64(binary.LittleEndian.Uint16(fr[14:16]))
		dia := float64(binary.LittleEndian.Uint16(fr[16:18]))
		out = append(out, bpReadings(f.At, sys, dia, float64(fr[18]), 0)...)
	}
}

// ---------- SY-G100: "55aa," then comma fields, packet ends at the 6th comma

type syg100BP struct{ buf string }

var reSYG100 = regexp.MustCompile(`(?i)55aa,`)

func (d *syg100BP) Feed(f transport.Frame) []model.Reading {
	d.buf += strings.NewReplacer("\r", "", "\n", "").Replace(string(f.Data))
	if len(d.buf) > maxBurst {
		d.buf = d.buf[len(d.buf)-maxBurst:]
	}
	var out []model.Reading
	for {
		loc := reSYG100.FindStringIndex(d.buf)
		if loc == nil {
			// Keep a tail that could be the start of a split marker.
			if len(d.buf) > 4 {
				d.buf = d.buf[len(d.buf)-4:]
			}
			return out
		}
		d.buf = d.buf[loc[0]:]
		fields := strings.SplitN(d.buf, ",", 7)
		if len(fields) < 7 {
			return out // sixth comma not here yet
		}
		d.buf = fields[6]
		out = append(out, bpReadings(f.At, num(fields[3]), num(fields[4]), num(fields[5]), 0)...)
	}
}

// ---------- Yuwell USB mode C: labelled fields separated by ';'

type yuwellBP struct{ burst }

var reYuwell = regexp.MustCompile(`(?i)\b(SYS|DIA|MAP|PR)\s*:\s*(\d+(?:\.\d+)?)`)

func (d *yuwellBP) Feed(f transport.Frame) []model.Reading {
	d.add(f.Data)
	var out []model.Reading
	for {
		i := bytes.IndexByte(d.buf, '\n')
		if i < 0 {
			return out
		}
		line := string(d.buf[:i])
		d.buf = d.buf[i+1:]
		out = append(out, d.parse(line, f.At)...)
	}
}

func (d *yuwellBP) Idle(at time.Time) []model.Reading { return d.parse(string(d.take()), at) }

// The OPD code took fields 3/5/6 by position; labels are what the sample
// frame actually carries, and they survive a missing Name or ID field.
func (d *yuwellBP) parse(s string, at time.Time) []model.Reading {
	v := map[string]float64{}
	for _, m := range reYuwell.FindAllStringSubmatch(s, -1) {
		v[strings.ToUpper(m[1])] = num(m[2])
	}
	return bpReadings(at, v["SYS"], v["DIA"], v["PR"], v["MAP"])
}
