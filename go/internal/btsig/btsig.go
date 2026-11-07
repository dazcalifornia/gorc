// Package btsig holds the Bluetooth SIG assigned numbers shared by the BLE
// transport and the GATT decoder. It is a leaf package so both can use it
// without an import cycle.
package btsig

import "strings"

// Health service UUIDs (16-bit assigned numbers).
const (
	SvcPulseOximeter   uint16 = 0x1822
	SvcWeightScale     uint16 = 0x181D
	SvcBodyComposition uint16 = 0x181B
	SvcThermometer     uint16 = 0x1809
	SvcBloodPressure   uint16 = 0x1810
	SvcHeartRate       uint16 = 0x180D
	SvcBattery         uint16 = 0x180F
	SvcDeviceInfo      uint16 = 0x180A
)

// HealthServices maps the services that make a BLE device a medical-device
// candidate to a display name. Used to label scan results and to suggest the
// ble-gatt decoder profile.
var HealthServices = map[uint16]string{
	SvcPulseOximeter:   "Pulse Oximeter",
	SvcWeightScale:     "Weight Scale",
	SvcBodyComposition: "Body Composition",
	SvcThermometer:     "Health Thermometer",
	SvcBloodPressure:   "Blood Pressure",
	SvcHeartRate:       "Heart Rate",
}

// ShortUUID extracts the 16-bit assigned number from a Bluetooth base UUID,
// accepting either the full 128-bit form or a bare 4-hex-digit string.
// ok is false for vendor-specific UUIDs, which have no assigned number.
func ShortUUID(u string) (uint16, bool) {
	s := strings.ToLower(strings.TrimSpace(u))
	switch len(s) {
	case 4: // already short, e.g. "2a5f"
	case 36:
		if !strings.HasPrefix(s, "0000") || !strings.HasSuffix(s, "-0000-1000-8000-00805f9b34fb") {
			return 0, false
		}
		s = s[4:8]
	default:
		return 0, false
	}
	var v uint16
	for _, c := range s {
		var d uint16
		switch {
		case c >= '0' && c <= '9':
			d = uint16(c - '0')
		case c >= 'a' && c <= 'f':
			d = uint16(c-'a') + 10
		default:
			return 0, false
		}
		v = v<<4 | d
	}
	return v, true
}

// IsVendor reports whether a UUID is vendor-specific rather than SIG-assigned.
// Vendor characteristics are the only sensible target for a blind write, which
// is how the BLE transport picks where to send wake-up commands.
func IsVendor(u string) bool {
	_, ok := ShortUUID(u)
	return !ok
}
