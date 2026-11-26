//go:build !windows

package transport

import "context"

func (h *USBHID) Describe() Availability {
	return Availability{Kind: KindUSBHID, Label: "USB HID", Note: "USB-HID devices are supported on Windows only."}
}

func (h *USBHID) Scan(ctx context.Context) ([]DeviceInfo, error) { return nil, ErrUnsupported }

func (h *USBHID) Open(ctx context.Context, dev DeviceInfo, opts OpenOptions) (Conn, error) {
	return nil, ErrUnsupported
}
