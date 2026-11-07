//go:build !darwin && !linux

package transport

import (
	"context"
	"fmt"
	"strings"
)

func btClassicDescribe() Availability {
	return Availability{
		Kind: KindBTClassic, Label: "Bluetooth classic (SPP)", Available: true,
		Note: "Pair the device in the OS Bluetooth settings; it then appears as a COM port that this transport opens.",
	}
}

func btClassicScan(ctx context.Context, s *Serial) ([]DeviceInfo, error) {
	return s.Scan(ctx)
}

func btClassicOpen(ctx context.Context, s *Serial, dev DeviceInfo, opts OpenOptions) (Conn, error) {
	if dev.Address == "" || !strings.HasPrefix(strings.ToUpper(dev.Address), "COM") {
		return nil, fmt.Errorf("%q has no serial port to open; pair it as a Serial Port Profile device first", dev.Name)
	}
	return s.Open(ctx, dev, opts)
}
