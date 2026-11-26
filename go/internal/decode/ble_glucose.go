package decode

import (
	"math"
	"time"

	"github.com/expert/go-connector/internal/model"
	"github.com/expert/go-connector/internal/transport"
)

// Glucose meters (Bluetooth SIG Glucose Service 0x1808), the Roche Accu-Chek
// role of the SMART EASY OPD kiosk (BleMonitor/BleProfiles.cs, role Accu;
// docs/Core/02-Bluetooth-BLE.md §2.6).
//
// The OPD code read byte 12 of the measurement as the value, which is the low
// byte of the concentration only when a time offset is present and the value
// is under 256. This decodes the record as the specification lays it out.

func init() {
	register(Profile{
		ID:    "ble-glucose",
		Label: "BLE — glucose meter (Accu-Chek and other 0x1808)",
		Note: "Bluetooth SIG glucose meters such as Roche Accu-Chek. They only send stored records when asked, so on connect this " +
			"writes 'report last record' (01 06) to the Record Access Control Point (0x2A52). Pair the meter in the OS first; " +
			"meters that ask for a PIN take it in the OS pairing dialog.",
		Transports: []transport.Kind{transport.KindBLE, transport.KindSim},
	}, func() Decoder { return &bleGlucose{} })
}

// racpReportLast is RACP opcode 0x01 "report stored records" with operator
// 0x06 "last record". The OPD's BleMonitor engine sent these two bytes; its
// legacy ble_lib engine sent the ASCII text "0x0101, 0x0106", which no meter
// would understand.
var racpReportLast = []byte{0x01, 0x06}

const uuidRACP = "00002a52-0000-1000-8000-00805f9b34fb"

type bleGlucose struct{ bleGATT }

func (d *bleGlucose) OnOpenGATT() []GATTWrite {
	return []GATTWrite{{Char: uuidRACP, Data: racpReportLast}}
}

// glucoseMeasurement decodes characteristic 0x2A18.
//
//	flags u8: bit0 time offset, bit1 concentration + type/location,
//	          bit2 unit (0 kg/L, 1 mol/L), bit3 sensor status, bit4 context follows
//	sequence u16, base time 7 bytes, [time offset s16], [SFLOAT concentration, u8 type/location]
func glucoseMeasurement(c *cursor, at time.Time) []model.Reading {
	flags, ok := c.u8()
	if !ok {
		return nil
	}
	c.skip(2) // sequence number
	taken := at
	if ts, ok := c.timestamp(); ok {
		taken = ts
	}
	if flags&0x01 != 0 {
		if off, ok := c.u16(); ok {
			taken = taken.Add(time.Duration(int16(off)) * time.Minute)
		}
	}
	if flags&0x02 == 0 || c.remaining() < 2 {
		return nil // a record without a concentration (context only)
	}
	v := c.sfloat()
	if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		return nil
	}

	value, unit := v*100000, "mg/dL" // kg/L → mg/dL
	if flags&0x04 != 0 {
		value, unit = v*1000, "mmol/L" // mol/L → mmol/L
	}
	if unit == "mg/dL" && (value < 10 || value > 600) {
		return nil
	}
	if unit == "mmol/L" && (value < 0.5 || value > 33.3) {
		return nil
	}
	// The record carries its own time: a meter reports stored readings, and
	// the newest may be from this morning, not from now.
	if taken.IsZero() || taken.After(at.Add(time.Minute)) {
		taken = at
	}
	return []model.Reading{{Metric: model.MetricGlucose, Value: round2(value), Unit: unit, Quality: model.QualityOK, At: taken}}
}
