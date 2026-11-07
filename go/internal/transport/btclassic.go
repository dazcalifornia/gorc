package transport

import "context"

// BTClassic is the classic-Bluetooth (RFCOMM / Serial Port Profile) transport.
//
// Classic BT is where the three desktop platforms diverge most. On macOS and
// Windows the OS Bluetooth stack already publishes a paired SPP device as an
// ordinary serial port, so the honest implementation is to reuse the serial
// transport and just filter and label the ports correctly. On Linux there is
// no such bridge, so we open an AF_BLUETOOTH/RFCOMM socket directly.
//
// Either way, the device above sees the same byte stream and the same decoder
// profiles work unchanged.
type BTClassic struct {
	serial *Serial
}

// NewBTClassic builds the classic Bluetooth transport for this platform.
func NewBTClassic() *BTClassic {
	return &BTClassic{serial: &Serial{
		kind:  KindBTClassic,
		label: "Bluetooth classic (SPP)",
		// Keep only ports the OS created for a Bluetooth link, and drop the
		// always-present inbound listener, which is not a device.
		filter: func(p portDetail) bool {
			return looksBluetooth(p.Name) && !isInboundBTPort(p.Name)
		},
	}}
}

func (b *BTClassic) Kind() Kind { return KindBTClassic }

func (b *BTClassic) Describe() Availability { return btClassicDescribe() }

func (b *BTClassic) Scan(ctx context.Context) ([]DeviceInfo, error) {
	return btClassicScan(ctx, b.serial)
}

func (b *BTClassic) Open(ctx context.Context, dev DeviceInfo, opts OpenOptions) (Conn, error) {
	return btClassicOpen(ctx, b.serial, dev, opts)
}

func isInboundBTPort(name string) bool {
	return name == "/dev/cu.Bluetooth-Incoming-Port" || name == "/dev/tty.Bluetooth-Incoming-Port"
}
