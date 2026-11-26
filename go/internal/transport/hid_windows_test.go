//go:build windows

package transport

import (
	"context"
	"testing"
)

// Every Windows machine has HID interfaces; Scan must enumerate them without
// error and list only ones it could open (keyboards and mice drop out).
func TestUSBHIDScanEnumerates(t *testing.T) {
	h := NewUSBHID()
	if !h.Describe().Available {
		t.Skip("hid.dll not available")
	}
	devs, err := h.Scan(context.Background())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, d := range devs {
		if d.Kind != KindUSBHID || d.Address == "" || d.Details["usb"] == "" {
			t.Fatalf("bad device %+v", d)
		}
	}
	t.Logf("%d openable HID interfaces", len(devs))
}
