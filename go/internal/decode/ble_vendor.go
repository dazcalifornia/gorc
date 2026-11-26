package decode

import (
	"encoding/binary"
	"strconv"
	"strings"

	"github.com/expert/go-connector/internal/btsig"
	"github.com/expert/go-connector/internal/model"
	"github.com/expert/go-connector/internal/transport"
)

// BLE devices whose payload is not the Bluetooth SIG layout, ported from the
// SMART EASY OPD kiosk (BleMonitor/BleProfiles.cs; docs/Core/02-Bluetooth-BLE.md
// §2.5 and §2.7). Neither was wired to a form in the OPD app, so neither has
// been run against hardware there either.

func init() {
	register(Profile{
		ID:    "scale-mi-ble",
		Label: "Scale — Xiaomi Mi body-composition scale (BLE)",
		Note: "Xiaomi Mi scales notify on Body Composition (0x2A9C) but not in the SIG layout: weight is a little-endian " +
			"uint16 at bytes 11–12 in units of 5 g. ble-gatt would misread these, so pick this profile for a Mi scale.",
		Transports: []transport.Kind{transport.KindBLE},
	}, func() Decoder { return &miScale{} })

	register(Profile{
		ID:    "thermo-htd-ble",
		Label: "Thermometer — HTD BLE gun (FFE1 ASCII)",
		Note: "HTD infrared thermometers: ASCII on vendor characteristic FFE1; the first four characters are a prefix and " +
			"the rest is the reading with a trailing 'C'.",
		Transports: []transport.Kind{transport.KindBLE},
	}, func() Decoder { return &htdThermo{} })
}

// ---------- Xiaomi Mi scale

type miScale struct {
	last  float64
	lastQ model.Quality
}

const charMiBodyComposition = 0x2A9C

func (d *miScale) Feed(f transport.Frame) []model.Reading {
	short, ok := btsig.ShortUUID(f.Source)
	if !ok || short != charMiBodyComposition || len(f.Data) <= 12 {
		return nil
	}
	kg := float64(binary.LittleEndian.Uint16(f.Data[11:13])) / 200
	if kg <= 0 || kg > 300 {
		return nil
	}
	// Byte 1 bit 5 is "stabilised" in the openScale Mi Scale 2 driver; the
	// OPD code read no flags and reported every in-progress weight. Marking a
	// settling weight as artifact keeps it visible without trusting it.
	// This bit is not from the OPD code and is unconfirmed on hardware here.
	quality := model.QualityOK
	if f.Data[1]&0x20 == 0 {
		quality = model.QualityArtifact
	}
	// The scale repeats itself while someone stands on it; forward changes.
	if kg == d.last && quality == d.lastQ {
		return nil
	}
	d.last, d.lastQ = kg, quality
	return []model.Reading{{Metric: model.MetricWeight, Value: round2(kg), Unit: "kg", Quality: quality, At: f.At}}
}

// ---------- HTD thermometer gun

type htdThermo struct{}

const charHTD = 0xFFE1

// Feed follows the OPD decode: ASCII, drop the first four characters, turn
// 'C' into a space, parse. Where the OPD forwarded the raw text when parsing
// failed, this drops the frame instead, and range-checks what it keeps.
func (d *htdThermo) Feed(f transport.Frame) []model.Reading {
	short, ok := btsig.ShortUUID(f.Source)
	if !ok || short != charHTD || len(f.Data) < 5 {
		return nil
	}
	body := strings.TrimSpace(strings.ReplaceAll(string(f.Data[4:]), "C", " "))
	v, err := strconv.ParseFloat(body, 64)
	if err != nil || v < 10 || v > 60 {
		return nil
	}
	return []model.Reading{{Metric: model.MetricTemperature, Value: round2(v), Unit: "°C", Quality: model.QualityOK, At: f.At}}
}
