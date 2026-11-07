// Package model holds the normalised, transport-agnostic vitals types that
// flow from a device decoder out to the websocket clients.
package model

import "time"

// Metric identifies a physiological quantity. Decoders emit these instead of
// device-specific field names so the frontend never has to know which vendor
// protocol produced a number.
type Metric string

const (
	MetricSpO2           Metric = "spo2"
	MetricPulseRate      Metric = "pulse_rate"
	MetricPerfusionIndex Metric = "perfusion_index"
	MetricPleth          Metric = "pleth"
	MetricSignalStrength Metric = "signal_strength"

	MetricWeight   Metric = "weight"
	MetricHeight   Metric = "height"
	MetricBMI      Metric = "bmi"
	MetricBodyFat  Metric = "body_fat"
	MetricLeanMass Metric = "lean_mass"

	MetricTemperature Metric = "temperature"
	MetricRespRate    Metric = "respiration_rate"

	MetricBPSystolic  Metric = "bp_systolic"
	MetricBPDiastolic Metric = "bp_diastolic"
	MetricBPMean      Metric = "bp_mean"

	MetricBattery Metric = "battery"
)

// Quality lets a decoder say "I produced a number but don't trust it yet",
// which is the normal state of a pulse oximeter for the first few seconds.
type Quality string

const (
	QualityOK        Quality = "ok"
	QualitySearching Quality = "searching"
	QualityArtifact  Quality = "artifact"
	QualityNoSensor  Quality = "no_sensor"
)

// Reading is a single decoded measurement.
type Reading struct {
	SessionID string    `json:"sessionId"`
	DeviceID  string    `json:"deviceId"`
	Metric    Metric    `json:"metric"`
	Value     float64   `json:"value"`
	Unit      string    `json:"unit"`
	Quality   Quality   `json:"quality"`
	At        time.Time `json:"at"`
	// Waveform marks high-rate samples (pleth) that the UI should chart rather
	// than display as a tile, and that we deliberately do not keep in history.
	Waveform bool `json:"waveform,omitempty"`
}

// Display returns a human label for a metric. Kept server-side so a new
// decoder metric shows up sensibly in the UI without a frontend change.
func (m Metric) Display() string {
	switch m {
	case MetricSpO2:
		return "SpO₂"
	case MetricPulseRate:
		return "Pulse rate"
	case MetricPerfusionIndex:
		return "Perfusion index"
	case MetricPleth:
		return "Plethysmogram"
	case MetricSignalStrength:
		return "Signal strength"
	case MetricWeight:
		return "Weight"
	case MetricHeight:
		return "Height"
	case MetricBMI:
		return "BMI"
	case MetricBodyFat:
		return "Body fat"
	case MetricLeanMass:
		return "Lean mass"
	case MetricTemperature:
		return "Temperature"
	case MetricRespRate:
		return "Respiration rate"
	case MetricBPSystolic:
		return "Systolic"
	case MetricBPDiastolic:
		return "Diastolic"
	case MetricBPMean:
		return "Mean arterial"
	case MetricBattery:
		return "Battery"
	}
	return string(m)
}
