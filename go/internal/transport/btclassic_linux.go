package transport

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

func btClassicDescribe() Availability {
	if _, err := os.Stat("/sys/class/bluetooth"); err != nil {
		return Availability{
			Kind: KindBTClassic, Label: "Bluetooth classic (RFCOMM)", Available: false,
			Note: "No Bluetooth adapter found under /sys/class/bluetooth.",
		}
	}
	return Availability{
		Kind: KindBTClassic, Label: "Bluetooth classic (RFCOMM)", Available: true,
		Note: "Opens an AF_BLUETOOTH/RFCOMM socket directly to a paired device. Pair first with bluetoothctl; " +
			"the RFCOMM channel defaults to 1 and can be overridden per device.",
	}
}

func btClassicScan(ctx context.Context, s *Serial) ([]DeviceInfo, error) {
	devs := pairedViaBluetoothctl(ctx)
	// Legacy /dev/rfcomm* bindings created by `rfcomm bind` still work and are
	// opened through the serial path.
	if ports, err := s.Scan(ctx); err == nil {
		devs = append(devs, ports...)
	}
	return devs, nil
}

func btClassicOpen(ctx context.Context, s *Serial, dev DeviceInfo, opts OpenOptions) (Conn, error) {
	if strings.HasPrefix(dev.Address, "/dev/") {
		return s.Open(ctx, dev, opts)
	}
	channel := 1
	if v, ok := dev.Details["channel"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			channel = n
		}
	}
	addr, err := parseBDAddr(dev.Address)
	if err != nil {
		return nil, err
	}

	fd, err := unix.Socket(unix.AF_BLUETOOTH, unix.SOCK_STREAM, unix.BTPROTO_RFCOMM)
	if err != nil {
		return nil, fmt.Errorf("rfcomm socket: %w", err)
	}
	if err := unix.Connect(fd, &unix.SockaddrRFCOMM{Addr: addr, Channel: uint8(channel)}); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("rfcomm connect to %s channel %d: %w", dev.Address, channel, err)
	}

	c := &rfcommConn{dev: dev, fd: fd, frames: make(chan Frame, 256), done: make(chan struct{})}
	go c.pump()
	return c, nil
}

// parseBDAddr converts "AA:BB:CC:DD:EE:FF" to the byte order the kernel wants,
// which is reversed relative to how the address is written.
func parseBDAddr(s string) ([6]byte, error) {
	var out [6]byte
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 6 {
		return out, fmt.Errorf("invalid Bluetooth address %q", s)
	}
	for i, p := range parts {
		v, err := strconv.ParseUint(p, 16, 8)
		if err != nil {
			return out, fmt.Errorf("invalid Bluetooth address %q: %w", s, err)
		}
		out[5-i] = byte(v)
	}
	return out, nil
}

// pairedViaBluetoothctl shells out rather than speaking D-Bus: for a proof of
// concept the dependency saving is worth more than the few milliseconds.
func pairedViaBluetoothctl(ctx context.Context) []DeviceInfo {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "bluetoothctl", "devices", "Paired").Output()
	if err != nil {
		return nil
	}
	var devs []DeviceInfo
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		fields := strings.SplitN(strings.TrimSpace(sc.Text()), " ", 3)
		if len(fields) < 2 || fields[0] != "Device" {
			continue
		}
		addr := fields[1]
		name := addr
		if len(fields) == 3 {
			name = fields[2]
		}
		devs = append(devs, DeviceInfo{
			ID:      "bt-classic:" + addr,
			Kind:    KindBTClassic,
			Name:    name,
			Address: addr,
			Details: map[string]string{"link": "RFCOMM", "channel": "1"},
		})
	}
	return devs
}

type rfcommConn struct {
	dev    DeviceInfo
	fd     int
	frames chan Frame

	mu        sync.Mutex
	err       error
	closed    bool
	done      chan struct{}
	closeOnce sync.Once
}

func (c *rfcommConn) Device() DeviceInfo   { return c.dev }
func (c *rfcommConn) Frames() <-chan Frame { return c.frames }

func (c *rfcommConn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *rfcommConn) Write(p []byte) error {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return errors.New("connection closed")
	}
	_, err := unix.Write(c.fd, p)
	return err
}

func (c *rfcommConn) pump() {
	defer close(c.frames)
	buf := make([]byte, 1024)
	for {
		n, err := unix.Read(c.fd, buf)
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
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
				continue
			}
			c.mu.Lock()
			if !c.closed {
				c.err = err
			}
			c.mu.Unlock()
			return
		}
		if n == 0 {
			return // peer closed
		}
	}
}

func (c *rfcommConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		close(c.done)
		// Shutdown unblocks the pump's read, which Close alone does not.
		unix.Shutdown(c.fd, unix.SHUT_RDWR)
		err = unix.Close(c.fd)
	})
	return err
}
