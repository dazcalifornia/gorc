package transport

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"tinygo.org/x/bluetooth"

	"github.com/expert/go-connector/internal/btsig"
)

// defaultScanWindow is how long we listen for advertisements when the caller
// does not impose a deadline. Long enough for a scale that advertises once a
// second, short enough that the UI does not feel stuck.
const defaultScanWindow = 6 * time.Second

// BLE is the Bluetooth Low Energy transport. It talks GATT through
// tinygo.org/x/bluetooth, which binds CoreBluetooth on macOS, BlueZ over D-Bus
// on Linux and WinRT on Windows.
type BLE struct {
	adapter *bluetooth.Adapter

	enableOnce sync.Once
	enableErr  error
	enabled    atomic.Bool

	mu       sync.Mutex
	scanning bool
	// seen caches the ScanResult for each device found by the last scan.
	//
	// This matters more than it looks: a bluetooth.Address is a MAC on Linux
	// but an opaque CoreBluetooth UUID on macOS, and on macOS that UUID is
	// only valid for this process. Keeping the value we were handed and
	// connecting with it is the one approach that works on every platform.
	seen map[string]bluetooth.ScanResult
	// conns routes disconnect callbacks back to the owning connection.
	conns map[string]*bleConn
}

// NewBLE builds the BLE transport. The radio is not touched until the first
// scan, so starting the server does not trigger an OS permission prompt.
func NewBLE() *BLE {
	return &BLE{
		adapter: bluetooth.DefaultAdapter,
		seen:    map[string]bluetooth.ScanResult{},
		conns:   map[string]*bleConn{},
	}
}

func (b *BLE) Kind() Kind { return KindBLE }

func (b *BLE) Describe() Availability {
	note := "Standards-based GATT. The first scan asks the OS for Bluetooth permission; on macOS grant it to the terminal or app running this server, or scans silently return nothing."
	if b.enabled.Load() {
		note = "Adapter powered on and ready."
	}
	return Availability{Kind: KindBLE, Label: "Bluetooth Low Energy", Available: true, Note: note}
}

func (b *BLE) enable() error {
	b.enableOnce.Do(func() {
		// Must be registered before the first Connect for the platform to
		// deliver disconnect events to us.
		b.adapter.SetConnectionStateHandler(b.onConnectionState)
		if err := b.adapter.Enable(); err != nil {
			b.enableErr = fmt.Errorf("enable Bluetooth adapter: %w", err)
			return
		}
		b.enabled.Store(true)
	})
	return b.enableErr
}

func (b *BLE) onConnectionState(dev bluetooth.Device, state bluetooth.ConnectionState) {
	if state.Connected {
		return
	}
	key := dev.Address.String()
	b.mu.Lock()
	c := b.conns[key]
	delete(b.conns, key)
	b.mu.Unlock()
	if c != nil {
		err := state.Err
		if err == nil {
			err = errors.New("device disconnected")
		}
		c.drop(err)
	}
}

func (b *BLE) Scan(ctx context.Context) ([]DeviceInfo, error) {
	if err := b.enable(); err != nil {
		return nil, err
	}

	b.mu.Lock()
	if b.scanning {
		b.mu.Unlock()
		return nil, errors.New("a BLE scan is already running")
	}
	b.scanning = true
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.scanning = false
		b.mu.Unlock()
	}()

	var (
		resultsMu sync.Mutex
		results   = map[string]DeviceInfo{}
	)

	scanDone := make(chan error, 1)
	go func() {
		scanDone <- b.adapter.Scan(func(_ *bluetooth.Adapter, res bluetooth.ScanResult) {
			key := res.Address.String()
			// The payload is only guaranteed valid inside this callback, so
			// everything we keep is copied out here.
			name := res.LocalName()
			var services []string
			for _, u := range res.ServiceUUIDs() {
				services = append(services, u.String())
			}

			b.mu.Lock()
			b.seen[key] = res
			b.mu.Unlock()

			resultsMu.Lock()
			results[key] = describeBLE(key, name, res.RSSI, services)
			resultsMu.Unlock()
		})
	}()

	window := defaultScanWindow
	if dl, ok := ctx.Deadline(); ok {
		if d := time.Until(dl); d > 0 && d < window {
			window = d
		}
	}
	timer := time.NewTimer(window)
	defer timer.Stop()

	select {
	case err := <-scanDone:
		// Scan returned on its own, which on these backends means it failed.
		if err != nil {
			return nil, fmt.Errorf("BLE scan: %w", err)
		}
	case <-timer.C:
		_ = b.adapter.StopScan()
		<-scanDone
	case <-ctx.Done():
		_ = b.adapter.StopScan()
		<-scanDone
	}

	resultsMu.Lock()
	defer resultsMu.Unlock()
	out := make([]DeviceInfo, 0, len(results))
	for _, d := range results {
		out = append(out, d)
	}
	// Strongest signal first: the device on the desk should top the list.
	sort.Slice(out, func(i, j int) bool { return out[i].RSSI > out[j].RSSI })
	return out, nil
}

func describeBLE(addr, name string, rssi int16, services []string) DeviceInfo {
	if name == "" {
		name = "(unnamed)"
	}
	details := map[string]string{"address": addr}

	// Label any recognised health service so the operator can tell a scale
	// from a pair of headphones without decoding UUIDs by eye.
	var labels []string
	suggested := ""
	for _, u := range services {
		if short, ok := btsig.ShortUUID(u); ok {
			if label, found := btsig.HealthServices[short]; found {
				labels = append(labels, label)
				suggested = "ble-gatt"
			}
		}
	}
	if len(labels) > 0 {
		details["profiles"] = strings.Join(labels, ", ")
	}

	return DeviceInfo{
		ID:               "ble:" + addr,
		Kind:             KindBLE,
		Name:             name,
		Address:          addr,
		RSSI:             rssi,
		Details:          details,
		Services:         services,
		SuggestedProfile: suggested,
	}
}

func (b *BLE) Open(ctx context.Context, dev DeviceInfo, opts OpenOptions) (Conn, error) {
	if err := b.enable(); err != nil {
		return nil, err
	}

	b.mu.Lock()
	res, ok := b.seen[dev.Address]
	b.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("device %s is not in the last scan result; scan again before connecting", dev.Address)
	}

	device, err := b.adapter.Connect(res.Address, bluetooth.ConnectionParams{})
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", dev.Name, err)
	}

	c := &bleConn{
		dev:    dev,
		device: device,
		in:     make(chan Frame, 512),
		frames: make(chan Frame, 512),
		done:   make(chan struct{}),
	}

	b.mu.Lock()
	b.conns[device.Address.String()] = c
	b.mu.Unlock()

	if err := c.subscribe(); err != nil {
		c.Close()
		return nil, err
	}
	go c.forward()
	return c, nil
}

// bleConn is one GATT connection.
//
// Notifications are delivered by the platform on its own thread, so callbacks
// only ever do a non-blocking push onto in; a forwarder goroutine owns frames
// and is the only thing that closes it. That keeps a slow consumer from
// stalling the Bluetooth stack and makes close races impossible.
type bleConn struct {
	dev    DeviceInfo
	device bluetooth.Device

	in     chan Frame
	frames chan Frame

	chars   []bluetooth.DeviceCharacteristic
	dropped atomic.Uint64

	mu        sync.Mutex
	err       error
	done      chan struct{}
	closeOnce sync.Once
}

func (c *bleConn) Device() DeviceInfo   { return c.dev }
func (c *bleConn) Frames() <-chan Frame { return c.frames }

func (c *bleConn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// subscribe walks every service and characteristic and enables notifications
// wherever the device allows it.
//
// We deliberately do not filter to a known list of characteristics. Enabling
// everything costs nothing, surfaces vendor characteristics in the raw frame
// log (which is how you reverse-engineer a device that is not SIG-compliant),
// and means teaching the decoder a new characteristic needs no transport
// change. Characteristics that do not support notify simply error and are
// skipped.
func (c *bleConn) subscribe() error {
	services, err := c.device.DiscoverServices(nil)
	if err != nil {
		return fmt.Errorf("discover services on %s: %w", c.dev.Name, err)
	}

	var serviceLabels []string
	subscribed := 0
	for _, svc := range services {
		su := svc.UUID().String()
		if short, ok := btsig.ShortUUID(su); ok {
			if label, found := btsig.HealthServices[short]; found {
				serviceLabels = append(serviceLabels, label)
			}
		}
		chars, err := svc.DiscoverCharacteristics(nil)
		if err != nil {
			continue // a service we cannot enumerate is not fatal
		}
		for _, ch := range chars {
			c.chars = append(c.chars, ch)
			uuid := ch.UUID().String()
			if err := ch.EnableNotifications(func(buf []byte) {
				// Copy: the platform reuses this buffer.
				data := make([]byte, len(buf))
				copy(data, buf)
				c.push(Frame{At: time.Now(), Source: uuid, Data: data})
			}); err == nil {
				subscribed++
			}
		}
	}

	if subscribed == 0 {
		return fmt.Errorf("connected to %s but no characteristic accepted notifications; "+
			"this device may need pairing, or may not be a GATT sensor", c.dev.Name)
	}
	if c.dev.Details == nil {
		c.dev.Details = map[string]string{}
	}
	c.dev.Details["subscribed"] = fmt.Sprintf("%d characteristics", subscribed)
	if len(serviceLabels) > 0 {
		c.dev.Details["profiles"] = strings.Join(serviceLabels, ", ")
	}
	return nil
}

func (c *bleConn) push(f Frame) {
	select {
	case c.in <- f:
	default:
		// Never block the Bluetooth callback thread. A dropped waveform
		// sample is better than a wedged radio; the count is reported so the
		// operator can see it happened.
		c.dropped.Add(1)
	}
}

func (c *bleConn) forward() {
	defer close(c.frames)
	for {
		select {
		case <-c.done:
			return
		case f := <-c.in:
			select {
			case c.frames <- f:
			case <-c.done:
				return
			}
		}
	}
}

// Write sends a command to the device.
//
// BLE has no single "write here" endpoint, so we target vendor-specific
// characteristics — the SIG health characteristics are notify-only, and a
// device that needs a wake-up command always exposes a 128-bit one for it.
func (c *bleConn) Write(p []byte) error {
	select {
	case <-c.done:
		return errors.New("connection closed")
	default:
	}
	var lastErr error
	tried := 0
	for _, ch := range c.chars {
		if !btsig.IsVendor(ch.UUID().String()) {
			continue
		}
		tried++
		if _, err := ch.WriteWithoutResponse(p); err == nil {
			return nil
		} else if _, err := ch.Write(p); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	if tried == 0 {
		return errors.New("device exposes no writable vendor characteristic")
	}
	return fmt.Errorf("no vendor characteristic accepted the write: %w", lastErr)
}

// Dropped reports notifications discarded because the consumer fell behind.
func (c *bleConn) Dropped() uint64 { return c.dropped.Load() }

// drop records an involuntary disconnect and tears the connection down.
func (c *bleConn) drop(err error) {
	c.mu.Lock()
	if c.err == nil {
		c.err = err
	}
	c.mu.Unlock()
	c.closeOnce.Do(func() { close(c.done) })
}

func (c *bleConn) Close() error {
	c.closeOnce.Do(func() { close(c.done) })
	return c.device.Disconnect()
}
