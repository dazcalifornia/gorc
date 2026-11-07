package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"time"

	"go.bug.st/serial"
	"go.bug.st/serial/enumerator"
)

// Serial is the USB / RS-232 transport. It is also the engine behind classic
// Bluetooth on macOS and Windows, where a paired SPP device is published as an
// ordinary serial port (see btclassic.go).
type Serial struct {
	// filter, when set, keeps only ports this returns true for. BTClassic
	// reuses Serial with a filter instead of duplicating the open/pump code.
	filter func(portDetail) bool
	kind   Kind
	label  string
	note   string
}

// portDetail is the subset of enumerator.PortDetails we care about, plus the
// port name, so filters can be written without importing the enumerator.
type portDetail struct {
	Name      string
	IsUSB     bool
	VID, PID  string
	Serial    string
	Product   string
	Bluetooth bool
}

// NewSerial returns the plain USB/serial transport.
func NewSerial() *Serial {
	return &Serial{
		kind:  KindSerial,
		label: "USB / serial",
		note:  "USB-CDC, FTDI, CP210x and RS-232 adapters. Pick the port and baud rate, then a decoder profile.",
		filter: func(p portDetail) bool {
			// Hide the always-present Bluetooth and debug pseudo-ports; they
			// are never a device and only clutter the list.
			return !p.Bluetooth && !strings.Contains(p.Name, "debug-console")
		},
	}
}

func (s *Serial) Kind() Kind { return s.kind }

func (s *Serial) Describe() Availability {
	return Availability{Kind: s.kind, Label: s.label, Available: true, Note: s.note}
}

func (s *Serial) Scan(ctx context.Context) ([]DeviceInfo, error) {
	ports, err := enumerator.GetDetailedPortsList()
	if err != nil {
		return nil, fmt.Errorf("enumerate serial ports: %w", err)
	}
	out := []DeviceInfo{}
	for _, p := range ports {
		d := portDetail{
			Name: p.Name, IsUSB: p.IsUSB, VID: p.VID, PID: p.PID,
			Serial: p.SerialNumber, Product: p.Product,
			Bluetooth: looksBluetooth(p.Name),
		}
		if s.filter != nil && !s.filter(d) {
			continue
		}
		out = append(out, s.describePort(d))
	}
	return out, nil
}

func (s *Serial) describePort(p portDetail) DeviceInfo {
	details := map[string]string{"port": p.Name}
	if p.IsUSB {
		details["usb"] = fmt.Sprintf("%s:%s", p.VID, p.PID)
		if p.Serial != "" {
			details["serialNumber"] = p.Serial
		}
	}
	name := p.Product
	if name == "" {
		name = friendlyPortName(p.Name)
	}
	return DeviceInfo{
		ID:      string(s.kind) + ":" + p.Name,
		Kind:    s.kind,
		Name:    name,
		Address: p.Name,
		Details: details,
	}
}

// friendlyPortName trims the platform prefix so the UI shows "usbserial-1410"
// rather than the full device path.
func friendlyPortName(name string) string {
	for _, prefix := range []string{"/dev/cu.", "/dev/tty.", "/dev/", ""} {
		if prefix != "" && strings.HasPrefix(name, prefix) {
			return strings.TrimPrefix(name, prefix)
		}
	}
	return name
}

func looksBluetooth(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "bluetooth") || strings.Contains(n, "-spp") ||
		strings.Contains(n, "rfcomm") || strings.Contains(n, "wirelessid")
}

func (s *Serial) Open(ctx context.Context, dev DeviceInfo, opts OpenOptions) (Conn, error) {
	baud := opts.BaudRate
	if baud == 0 {
		baud = 9600
	}
	port := dev.Address
	if port == "" {
		return nil, errors.New("device has no serial port path")
	}
	// On macOS, /dev/tty.* blocks on open waiting for carrier detect; /dev/cu.*
	// is the call-out device that opens immediately. Prefer it.
	if runtime.GOOS == "darwin" {
		port = strings.Replace(port, "/dev/tty.", "/dev/cu.", 1)
	}

	p, err := serial.Open(port, &serial.Mode{
		BaudRate: baud,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	})
	if err != nil {
		return nil, fmt.Errorf("open %s at %d baud: %w", port, baud, err)
	}
	// A read timeout keeps the pump goroutine responsive to Close even when a
	// device has gone quiet, which is the normal state of a scale between
	// weighings.
	if err := p.SetReadTimeout(250 * time.Millisecond); err != nil {
		p.Close()
		return nil, fmt.Errorf("set read timeout: %w", err)
	}

	c := &serialConn{dev: dev, port: p, frames: make(chan Frame, 256), done: make(chan struct{})}
	go c.pump()
	return c, nil
}

type serialConn struct {
	dev    DeviceInfo
	port   serial.Port
	frames chan Frame

	mu        sync.Mutex
	err       error
	closed    bool
	done      chan struct{}
	closeOnce sync.Once
}

func (c *serialConn) Device() DeviceInfo   { return c.dev }
func (c *serialConn) Frames() <-chan Frame { return c.frames }

func (c *serialConn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *serialConn) Write(p []byte) error {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return errors.New("connection closed")
	}
	_, err := c.port.Write(p)
	return err
}

func (c *serialConn) pump() {
	defer close(c.frames)
	buf := make([]byte, 1024)
	for {
		select {
		case <-c.done:
			return
		default:
		}
		n, err := c.port.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			select {
			case c.frames <- Frame{At: time.Now(), Source: "rx", Data: data}:
			case <-c.done:
				return
			}
		}
		if err != nil {
			// A timeout with no bytes is the expected idle case, not a fault.
			if errors.Is(err, io.EOF) || isTimeout(err) {
				continue
			}
			c.mu.Lock()
			if !c.closed {
				c.err = err
			}
			c.mu.Unlock()
			return
		}
	}
}

func isTimeout(err error) bool {
	type timeouter interface{ Timeout() bool }
	var t timeouter
	return errors.As(err, &t) && t.Timeout()
}

func (c *serialConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		close(c.done)
		err = c.port.Close()
	})
	return err
}
