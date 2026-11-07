package decode

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/expert/go-connector/internal/model"
	"github.com/expert/go-connector/internal/transport"
)

func init() {
	register(Profile{
		ID:          "ascii-lines",
		Label:       "ASCII line protocol — scale / stadiometer / generic",
		Note:        "Line-oriented serial devices: A&D, CAS, Mettler, Seca and Charder weight/height columns, plus any device that prints 'WT:70.5 HT:170 BMI:24.4'. Tolerant of vendor prefixes and whitespace.",
		Transports:  []transport.Kind{transport.KindSerial, transport.KindBTClassic, transport.KindSim},
		DefaultBaud: 9600,
	}, func() Decoder { return &asciiLines{last: map[model.Metric]float64{}} })
}

// asciiLines parses newline-delimited ASCII measurement lines.
//
// Vendors agree on almost nothing here except "print a number and a unit", so
// rather than hard-coding one layout we recognise two shapes: labelled fields
// (WT:70.5) and bare value+unit pairs (ST,GS,+  70.55 kg), the latter being
// the de-facto A&D/CAS/Mettler format most clinical scales speak.
type asciiLines struct {
	buf  []byte
	last map[model.Metric]float64
	seen time.Time
}

// maxLine bounds the buffer for devices that stream without any terminator.
const maxLine = 512

var (
	reLabelled = regexp.MustCompile(`(?i)\b(wt|weight|ht|height|bmi|fat|bf|bodyfat|lean|temp|spo2|pr|hr|pulse)\b\s*[:=]?\s*([-+]?\d+(?:\.\d+)?)\s*(kgs?|lbs?|cm|mm|in(?:ch(?:es)?)?|%|bpm|°?[cf])?`)
	reValUnit  = regexp.MustCompile(`(?i)([-+]?\d+(?:\.\d+)?)\s*(kgs?|lbs?|cm|mm|in(?:ch(?:es)?)?|bpm|%|°\s*[cf]|deg\s*[cf]|g)\b`)
	reStable   = regexp.MustCompile(`(?i)^\s*(st|us|ul|ol)\b`)
)

func (d *asciiLines) Feed(f transport.Frame) []model.Reading {
	d.buf = append(d.buf, f.Data...)
	var out []model.Reading

	for {
		i := bytes.IndexAny(d.buf, "\r\n")
		if i < 0 {
			if len(d.buf) > maxLine {
				out = append(out, d.line(string(d.buf), f.At)...)
				d.buf = d.buf[:0]
			}
			return out
		}
		line := string(d.buf[:i])
		d.buf = d.buf[i+1:]
		out = append(out, d.line(line, f.At)...)
	}
}

func (d *asciiLines) line(line string, at time.Time) []model.Reading {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}

	// A&D-style status prefix: ST = stable, US = unstable, OL/UL = over/under
	// load. An unstable sample is a real reading that has not settled yet.
	quality := model.QualityOK
	if m := reStable.FindStringSubmatch(line); m != nil && !strings.EqualFold(m[1], "st") {
		quality = model.QualityArtifact
	}

	var out []model.Reading
	emit := func(metric model.Metric, value float64, unit string) {
		// Clinical scales repeat the same line many times a second while the
		// patient stands still; only forward movement plus a 2 s heartbeat.
		if prev, ok := d.last[metric]; ok && prev == value && at.Sub(d.seen) < 2*time.Second {
			return
		}
		d.last[metric] = value
		out = append(out, model.Reading{
			Metric: metric, Value: round2(value), Unit: unit, Quality: quality, At: at,
		})
	}

	if ms := reLabelled.FindAllStringSubmatch(line, -1); len(ms) > 0 {
		for _, m := range ms {
			v, err := strconv.ParseFloat(m[2], 64)
			if err != nil {
				continue
			}
			metric, value, unit := byLabel(m[1], v, m[3])
			if metric != "" {
				emit(metric, value, unit)
			}
		}
	} else {
		for _, m := range reValUnit.FindAllStringSubmatch(line, -1) {
			v, err := strconv.ParseFloat(m[1], 64)
			if err != nil {
				continue
			}
			metric, value, unit := byUnit(v, m[2])
			if metric != "" {
				emit(metric, value, unit)
			}
		}
	}
	if len(out) > 0 {
		d.seen = at
	}
	return out
}

// byLabel resolves an explicitly labelled field. The label decides the metric;
// the unit (when the device bothered to print one) decides the scaling.
func byLabel(label string, v float64, unit string) (model.Metric, float64, string) {
	u := normUnit(unit)
	switch strings.ToLower(label) {
	case "wt", "weight":
		value, unit := convMass(v, u)
		return model.MetricWeight, value, unit
	case "ht", "height":
		value, unit := convLength(v, u)
		return model.MetricHeight, value, unit
	case "bmi":
		return model.MetricBMI, v, "kg/m²"
	case "fat", "bf", "bodyfat":
		return model.MetricBodyFat, v, "%"
	case "lean":
		value, unit := convMass(v, u)
		return model.MetricLeanMass, value, unit
	case "temp":
		if u == "f" {
			return model.MetricTemperature, v, "°F"
		}
		return model.MetricTemperature, v, "°C"
	case "spo2":
		return model.MetricSpO2, v, "%"
	case "pr", "hr", "pulse":
		return model.MetricPulseRate, v, "bpm"
	}
	return "", 0, ""
}

// byUnit resolves a bare number by its unit, which is all the A&D/CAS family
// gives us.
func byUnit(v float64, unit string) (model.Metric, float64, string) {
	switch normUnit(unit) {
	case "kg", "lb", "g":
		value, u := convMass(v, normUnit(unit))
		return model.MetricWeight, value, u
	case "cm", "mm", "in":
		value, u := convLength(v, normUnit(unit))
		return model.MetricHeight, value, u
	case "bpm":
		return model.MetricPulseRate, v, "bpm"
	case "c":
		return model.MetricTemperature, v, "°C"
	case "f":
		return model.MetricTemperature, v, "°F"
	case "%":
		return model.MetricBodyFat, v, "%"
	}
	return "", 0, ""
}

func normUnit(u string) string {
	u = strings.ToLower(strings.TrimSpace(u))
	u = strings.NewReplacer("°", "", "deg", "", " ", "").Replace(u)
	switch u {
	case "kgs":
		return "kg"
	case "lbs":
		return "lb"
	case "inch", "inches":
		return "in"
	}
	return u
}

// convMass normalises to kilograms, keeping pounds as-is since that is a
// clinically meaningful display unit rather than a quirk of the wire format.
func convMass(v float64, u string) (float64, string) {
	switch u {
	case "g":
		return v / 1000, "kg"
	case "lb":
		return v, "lb"
	default:
		return v, "kg"
	}
}

// convLength normalises to centimetres, keeping inches as-is.
func convLength(v float64, u string) (float64, string) {
	switch u {
	case "mm":
		return v / 10, "cm"
	case "in":
		return v, "in"
	default:
		return v, "cm"
	}
}
