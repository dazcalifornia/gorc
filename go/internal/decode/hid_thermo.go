package decode

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/expert/go-connector/internal/model"
	"github.com/expert/go-connector/internal/transport"
)

// USB-HID thermometer VID 0C45 / PID 7702, the one the SMART EASY OPD kiosk
// reads through GenericHid.dll's clsTemp. There is no source for that DLL;
// this follows its IL (ExchangeInputAndOutputReports, GetInputReportData and
// AccessForm, disassembled with ildasm):
//
//   - every 500 ms write an all-zero output report, then read one input report
//   - the report minus byte 0 (report ID) is read as ASCII; spaces, NULs and
//     CRs are dropped
//   - a report starting "^" carries the whole degrees and resets the reading:
//     "^000Tb" means no measurement; "^001<digits>" gives the degrees as the
//     digits; any other "^…" gives them as its last two digits
//   - a report ending "C" carries the tenths, and completes the reading only
//     if whole degrees are pending, giving "36" + "." + "5"
//
// The OPD wrote whatever that produced; this range-checks it (10–60 °C, the
// same bound as its serial thermometer).

func init() {
	register(Profile{
		ID:    "thermo-hid-0c45",
		Label: "Thermometer — USB HID 0C45:7702 (OPD kiosk)",
		Note: "The USB thermometer the OPD kiosk reads through GenericHid.dll. Polled every 500 ms with an empty output report; " +
			"the reading arrives as two reports, whole degrees ('^…') then tenths ('…C'). Windows only.",
		Transports: []transport.Kind{transport.KindUSBHID},
	}, func() Decoder { return &hidThermo{} })
}

var reNonDigit = regexp.MustCompile(`[^0-9]`)

type hidThermo struct {
	// whole holds "36." once the degrees report has arrived; "" otherwise.
	whole string
}

func (d *hidThermo) OnOpen() [][]byte { return nil }

// Keepalive is the 500 ms poll: report ID 0, padded with zeros by the HID
// transport to the device's output report length.
func (d *hidThermo) Keepalive() ([]byte, time.Duration) { return []byte{0x00}, 500 * time.Millisecond }

func (d *hidThermo) Feed(f transport.Frame) []model.Reading {
	if len(f.Data) < 2 {
		return nil
	}
	text := strings.NewReplacer(" ", "", "\x00", "", "\r", "").Replace(strings.TrimSpace(string(f.Data[1:])))

	switch {
	case strings.HasPrefix(text, "^"):
		d.whole = ""
		switch {
		case strings.HasPrefix(text, "^000Tb"):
			// "0.0" in the OPD, which never completes: no measurement.
		case strings.HasPrefix(text, "^001"):
			if n, err := strconv.Atoi(reNonDigit.ReplaceAllString(strings.Replace(text, "^001", "", 1), "")); err == nil {
				d.whole = strconv.Itoa(n) + "."
			}
		default:
			digits := reNonDigit.ReplaceAllString(text, "")
			if len(digits) > 2 {
				digits = digits[len(digits)-2:]
			}
			if n, err := strconv.Atoi(digits); err == nil {
				d.whole = strconv.Itoa(n) + "."
			}
		}
		return nil

	case strings.HasSuffix(text, "C"):
		if d.whole == "" {
			return nil
		}
		tenths := reNonDigit.ReplaceAllString(text, "")
		if tenths == "" {
			tenths = "0"
		}
		v, err := strconv.ParseFloat(d.whole+tenths, 64)
		d.whole = ""
		if err != nil || v < 10 || v > 60 {
			return nil
		}
		return []model.Reading{{Metric: model.MetricTemperature, Value: v, Unit: "°C", Quality: model.QualityOK, At: f.At}}
	}
	return nil
}
