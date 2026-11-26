package transport

import "testing"

const (
	svcFFE0 = "0000ffe0-0000-1000-8000-00805f9b34fb"
	svcBP   = "00001810-0000-1000-8000-00805f9b34fb"
)

func TestDescribeBLESuggestsYuwellForFFE0(t *testing.T) {
	d := describeBLE("aa:bb", "Yuwell", -60, []string{svcFFE0})
	if d.SuggestedProfile != "spo2-yuwell-ble" {
		t.Fatalf("suggested %q", d.SuggestedProfile)
	}
}

func TestDescribeBLEPrefersStandardHealthService(t *testing.T) {
	d := describeBLE("aa:bb", "BP", -60, []string{svcFFE0, svcBP})
	if d.SuggestedProfile != "ble-gatt" {
		t.Fatalf("suggested %q", d.SuggestedProfile)
	}
}
