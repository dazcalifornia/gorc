package decode

import (
	"time"

	"github.com/expert/go-connector/internal/btsig"
	"github.com/expert/go-connector/internal/model"
	"github.com/expert/go-connector/internal/transport"
)

// Yuwell BLE pulse oximeter, ported from the SMART EASY OPD kiosk
// (BleMonitor/BleProfiles.cs, role Sat; docs/Core/02-Bluetooth-BLE.md §2.2).
//
// It is not a SIG Pulse Oximeter (0x1822): it notifies on the vendor service
// FFE0, characteristic FFE4 (FFE1 on some units). The OPD code reads only two
// bytes of any notification longer than five, pulse at [4] and SpO2 at [5],
// and checks no header or packet type. The rest of the layout is unknown, so
// the range checks here are what stop another packet type from being read as
// a measurement.

const (
	yuwellCharMain   uint16 = 0xFFE4
	yuwellCharLegacy uint16 = 0xFFE1
)

func init() {
	register(Profile{
		ID:    "spo2-yuwell-ble",
		Label: "Pulse oximeter — Yuwell BLE (FFE0 / FFE4)",
		Note: "Yuwell Bluetooth fingertip oximeters as used by the OPD kiosk: vendor service FFE0, notifications on FFE4 (or FFE1) " +
			"with pulse at byte 4 and SpO₂ at byte 5. Streams while a finger is in.",
		Transports: []transport.Kind{transport.KindBLE, transport.KindSim},
	}, func() Decoder { return &yuwellSpO2{} })
}

type yuwellSpO2 struct {
	lastSpO2, lastPulse float64
	lastEmit            time.Time
}

func (d *yuwellSpO2) Feed(f transport.Frame) []model.Reading {
	short, ok := btsig.ShortUUID(f.Source)
	if !ok || (short != yuwellCharMain && short != yuwellCharLegacy) {
		return nil // battery, device info, or anything else we subscribed to
	}
	if len(f.Data) <= 5 {
		return nil
	}
	pulse, spo2 := float64(f.Data[4]), float64(f.Data[5])

	// The MB1280 form accepted SpO2 1–100; 0 and 127/255 are what finger-out
	// and "no reading yet" look like on oximeters of this kind.
	spo2OK := spo2 >= 1 && spo2 <= 100
	pulseOK := pulse >= 20 && pulse <= 250
	if !spo2OK && !pulseOK {
		return nil
	}

	// The device notifies continuously; forward changes plus a 1 s heartbeat,
	// the same policy as the BCI stream.
	if spo2 == d.lastSpO2 && pulse == d.lastPulse && f.At.Sub(d.lastEmit) < time.Second {
		return nil
	}
	d.lastSpO2, d.lastPulse, d.lastEmit = spo2, pulse, f.At

	var out []model.Reading
	if spo2OK {
		out = append(out, model.Reading{Metric: model.MetricSpO2, Value: spo2, Unit: "%", Quality: model.QualityOK, At: f.At})
	}
	if pulseOK {
		out = append(out, model.Reading{Metric: model.MetricPulseRate, Value: pulse, Unit: "bpm", Quality: model.QualityOK, At: f.At})
	}
	return out
}
