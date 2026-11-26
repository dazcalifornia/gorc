package decode

import (
	"bytes"
	"math"
	"regexp"
	"strconv"
	"time"

	"github.com/expert/go-connector/internal/model"
	"github.com/expert/go-connector/internal/transport"
)

// Thermometer and pulse-oximeter protocols ported from the SMART EASY OPD
// kiosk (docs/Core/01-Serial-COM-HID.md, sections 2.3 and 2.5).

func init() {
	register(Profile{
		ID: "thermo-serial", Label: "Thermometer — serial (binary FE FD or 'T body' text)",
		Note:       "CRLF-terminated thermometer lines: binary 'FE FD 1A 01 <hi> <lo> 0D 0A' (big-endian tenths of °C) or text 'T body = 36.29 C'. Text is checked first. 9600 8N1.",
		Transports: serialOnly, DefaultBaud: 9600,
	}, func() Decoder { return &thermoSerial{} })

	register(Profile{
		ID: "spo2-kscm01", Label: "Pulse oximeter — HealForce kscm01 (AA 55)",
		Note:       "HealForce kscm01 module: 'AA 55' frames, type 0x53 carries SpO₂ / pulse / PI about once a second, 0x52 the plethysmogram. Started with 'AA 55 FF 02 01 CA'. 38400 8N1.",
		Transports: serialOnly, DefaultBaud: 38400,
	}, func() Decoder { return &kscm01{} })
}

// ---------- serial thermometer

type thermoSerial struct{ buf []byte }

var reTBody = regexp.MustCompile(`T body\s*=\s*(\d+\.\d+)`)

func (d *thermoSerial) Feed(f transport.Frame) []model.Reading {
	d.buf = append(d.buf, f.Data...)
	if len(d.buf) > 512 {
		d.buf = append([]byte(nil), d.buf[len(d.buf)-512:]...)
	}
	var out []model.Reading
	for {
		i := bytes.Index(d.buf, []byte("\r\n"))
		if i < 0 {
			return out
		}
		rec := d.buf[:i]
		d.buf = d.buf[i+2:]
		if r, ok := thermoRecord(rec, f.At); ok {
			out = append(out, r)
		}
	}
}

// thermoRecord reads one CRLF-terminated record. Text goes first: a text line
// also ends in CRLF, and read as binary its last two characters would make a
// nonsense temperature (the OPD quirk Q11).
func thermoRecord(rec []byte, at time.Time) (model.Reading, bool) {
	var c float64
	if ms := reTBody.FindAllSubmatch(rec, -1); len(ms) > 0 {
		v, err := strconv.ParseFloat(string(ms[len(ms)-1][1]), 64)
		if err != nil {
			return model.Reading{}, false
		}
		c = math.Round(v*10) / 10
	} else {
		if len(rec) < 2 {
			return model.Reading{}, false
		}
		c = float64(uint16(rec[len(rec)-2])<<8|uint16(rec[len(rec)-1])) / 10
	}
	if c < 10 || c > 60 {
		return model.Reading{}, false
	}
	return model.Reading{Metric: model.MetricTemperature, Value: c, Unit: "°C", Quality: model.QualityOK, At: at}, true
}

// ---------- HealForce kscm01 pulse oximeter

type kscm01 struct{ buf []byte }

var kscm01Start = []byte{0xAA, 0x55, 0xFF, 0x02, 0x01, 0xCA}

func (d *kscm01) OnOpen() [][]byte                   { return [][]byte{kscm01Start} }
func (d *kscm01) Keepalive() ([]byte, time.Duration) { return nil, 0 }

// Feed frames on the AA 55 sync word and byte 3, which both sample frames
// confirm is the count of bytes that follow it (07 → 11-byte frame, 03 → 7).
// The trailing byte looks like a checksum; its algorithm is unknown, so like
// the OPD code we do not check it.
func (d *kscm01) Feed(f transport.Frame) []model.Reading {
	d.buf = append(d.buf, f.Data...)
	var out []model.Reading
	for {
		i := bytes.Index(d.buf, []byte{0xAA, 0x55})
		if i < 0 {
			if n := len(d.buf); n > 0 && d.buf[n-1] == 0xAA {
				d.buf = d.buf[n-1:]
			} else {
				d.buf = d.buf[:0]
			}
			return out
		}
		d.buf = d.buf[i:]
		if len(d.buf) < 4 {
			return out
		}
		size := 4 + int(d.buf[3])
		if size > 64 { // not a real length: resync past this sync word
			d.buf = d.buf[2:]
			continue
		}
		if len(d.buf) < size {
			return out
		}
		fr := d.buf[:size]
		d.buf = d.buf[size:]
		out = append(out, d.frame(fr, f.At)...)
	}
}

func (d *kscm01) frame(fr []byte, at time.Time) []model.Reading {
	r := func(m model.Metric, v float64, unit string) model.Reading {
		return model.Reading{Metric: m, Value: v, Unit: unit, Quality: model.QualityOK, At: at}
	}
	switch fr[2] {
	case 0x53: // measurement — the only type that carries SpO2 (OPD quirk Q12)
		if len(fr) < 9 {
			return nil
		}
		var out []model.Reading
		if spo2 := float64(fr[5]); spo2 > 0 && spo2 <= 100 {
			out = append(out, r(model.MetricSpO2, spo2, "%"))
		}
		if pulse := float64(fr[6]); pulse > 0 && pulse < 255 {
			out = append(out, r(model.MetricPulseRate, pulse, "bpm"))
		}
		out = append(out, r(model.MetricPerfusionIndex, float64(fr[8])/10, "%"))
		return out
	case 0x52: // plethysmogram sample; 0 is also what "no finger" sends
		if len(fr) < 6 {
			return nil
		}
		p := r(model.MetricPleth, float64(fr[5]), "")
		p.Waveform = true
		return []model.Reading{p}
	}
	return nil
}
