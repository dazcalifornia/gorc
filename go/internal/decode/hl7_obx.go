package decode

import (
	"math"
	"strings"

	"github.com/expert/go-connector/internal/model"
	"github.com/expert/go-connector/internal/transport"
)

// HL7 results from bedside monitors, ported from the SMART EASY OPD kiosk's
// Hl7Service (docs/Core/04-HL7-Monitor.md §2.1–2.2, "group A"). The HL7
// transport delivers one message per frame and handles the ACK.
//
// Matching is on OBX-3 only: a substring of the upper-cased field for MDC
// names, or an exact match on its first component for short codes (IM70 and
// similar). The first rule that matches wins, in the OPD's order. Differences
// from the OPD, all deliberate:
//   - MAP is reported (bp_mean); the OPD discarded it.
//   - ECG heart rate is its own metric and is not copied into pulse_rate when
//     a message has no pulse; the OPD did that only to unblock its auto-save.
//   - Values are range-checked, not just "> 0".
//
// The OPD's positional parsers (forms HM and KPHIS, "group C") are not
// ported: they read OBX by segment index and disagree with each other on what
// index 7 means, so they only fit the specific monitor each site had.

func init() {
	register(Profile{
		ID:    "hl7-obx",
		Label: "HL7 monitor — OBX results (MDC names or short codes)",
		Note: "Bedside monitors pushing HL7 ORU over TCP. Reads SpO₂, pulse, ECG heart rate, respiration, temperature and NIBP " +
			"from OBX-3 names (MDC_PULS_OXIM_SAT_O2, MDC_PRESS_BLD_NONINV_SYS, …) or short codes (SpO2, PR, HR, RR, T1, NIBP_SYS, …).",
		Transports: []transport.Kind{KindHL7},
	}, func() Decoder { return &hl7OBX{} })
}

// KindHL7 mirrors transport.KindHL7 so the profile can name it.
const KindHL7 = transport.KindHL7

type hl7Rule struct {
	metric   model.Metric
	contains []string // substrings of upper-cased OBX-3
	codes    []string // exact first component, upper-cased
}

// hl7Rules is Hl7Service's table, in its order.
var hl7Rules = []hl7Rule{
	{model.MetricSpO2, []string{"MDC_PULS_OXIM_SAT_O2"}, []string{"SPO2", "SAO2"}},
	{model.MetricHeartRate, []string{"MDC_ECG_HEART_RATE"}, []string{"HR", "ECG_HR"}},
	{model.MetricPulseRate, []string{"MDC_PULS_OXIM_PULS_RATE", "MDC_PULS_RATE_NON_INV"}, []string{"PR", "NIBP_PR", "PULSE"}},
	{model.MetricRespRate, []string{"MDC_PULS_OXIM_PLETH_RESP_RATE", "MDC_TTHOR_RESP_RATE", "MDC_THOR_RESP_RATE", "MDC_RESP_RATE"}, []string{"RR", "RESP"}},
	{model.MetricTemperature, []string{"MDC_TEMP"}, []string{"T1", "T2", "TEMP"}},
	{model.MetricBPSystolic, []string{"NONINV_SYS", "_CUFF_SYS"}, []string{"NIBP_SYS", "SYS", "BP_SYS"}},
	{model.MetricBPDiastolic, []string{"NONINV_DIA", "_CUFF_DIA"}, []string{"NIBP_DIA", "DIA", "BP_DIA"}},
	{model.MetricBPMean, []string{"NONINV_MEAN", "_CUFF_MEAN"}, []string{"NIBP_MAP", "MAP"}},
}

// hl7Ranges drops values no patient produces; a misparse, not a reading.
var hl7Ranges = map[model.Metric][2]float64{
	model.MetricSpO2:        {1, 100},
	model.MetricHeartRate:   {15, 300},
	model.MetricPulseRate:   {15, 300},
	model.MetricRespRate:    {1, 120},
	model.MetricTemperature: {25, 45},
	model.MetricBPSystolic:  {40, 300},
	model.MetricBPDiastolic: {20, 200},
	model.MetricBPMean:      {20, 250},
}

var hl7Units = map[model.Metric]string{
	model.MetricSpO2: "%", model.MetricHeartRate: "bpm", model.MetricPulseRate: "bpm",
	model.MetricRespRate: "rpm", model.MetricTemperature: "°C",
	model.MetricBPSystolic: "mmHg", model.MetricBPDiastolic: "mmHg", model.MetricBPMean: "mmHg",
}

type hl7OBX struct{}

func (d *hl7OBX) Feed(f transport.Frame) []model.Reading {
	// Later OBX for the same metric overwrite earlier ones, as in the OPD
	// (an NIBP pulse after the SpO2 pulse wins), so collect then emit.
	latest := map[model.Metric]float64{}
	var order []model.Metric

	for _, seg := range strings.FieldsFunc(string(f.Data), func(r rune) bool { return r == '\r' || r == '\n' }) {
		seg = strings.TrimSpace(strings.NewReplacer("\x0b", "", "\x1c", "").Replace(seg))
		if !strings.HasPrefix(seg, "OBX") {
			continue
		}
		fields := strings.Split(seg, "|")
		if len(fields) <= 5 {
			continue
		}
		metric, ok := hl7Match(strings.ToUpper(fields[3]))
		if !ok {
			continue
		}
		v := num(fields[5])
		if v <= 0 {
			continue
		}
		if metric == model.MetricTemperature {
			// Some monitors send tenths ("365"). Truncate to one decimal, as
			// the OPD did, rather than round.
			if v > 100 {
				v /= 10
			}
			v = math.Trunc(v*10) / 10
		}
		if r := hl7Ranges[metric]; v < r[0] || v > r[1] {
			continue
		}
		if _, seen := latest[metric]; !seen {
			order = append(order, metric)
		}
		latest[metric] = v
	}

	out := make([]model.Reading, 0, len(order))
	for _, m := range order {
		out = append(out, model.Reading{
			Metric: m, Value: round2(latest[m]), Unit: hl7Units[m], Quality: model.QualityOK, At: f.At,
		})
	}
	return out
}

func hl7Match(obx3 string) (model.Metric, bool) {
	id := strings.TrimSpace(strings.SplitN(obx3, "^", 2)[0])
	for _, r := range hl7Rules {
		for _, s := range r.contains {
			if strings.Contains(obx3, s) {
				return r.metric, true
			}
		}
		for _, c := range r.codes {
			if id == c {
				return r.metric, true
			}
		}
	}
	return "", false
}
