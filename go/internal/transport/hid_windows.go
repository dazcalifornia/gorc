//go:build windows

package transport

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The HID class driver is reached through hid.dll and plain file I/O on the
// device interface path: no cgo, no hidapi. Each ReadFile returns exactly one
// input report and each WriteFile sends one output report, both sized by the
// device's HIDP_CAPS.

var (
	modHID                     = windows.NewLazySystemDLL("hid.dll")
	procHidD_GetHidGuid        = modHID.NewProc("HidD_GetHidGuid")
	procHidD_GetAttributes     = modHID.NewProc("HidD_GetAttributes")
	procHidD_GetProductString  = modHID.NewProc("HidD_GetProductString")
	procHidD_GetPreparsedData  = modHID.NewProc("HidD_GetPreparsedData")
	procHidD_FreePreparsedData = modHID.NewProc("HidD_FreePreparsedData")
	procHidP_GetCaps           = modHID.NewProc("HidP_GetCaps")
)

const hidpStatusSuccess = 0x00110000

type hiddAttributes struct {
	Size      uint32
	VendorID  uint16
	ProductID uint16
	Version   uint16
}

type hidpCaps struct {
	Usage                   uint16
	UsagePage               uint16
	InputReportByteLength   uint16
	OutputReportByteLength  uint16
	FeatureReportByteLength uint16
	Reserved                [17]uint16
	Counts                  [10]uint16
}

type hidInfo struct {
	vidpid  string
	product string
	caps    hidpCaps
}

func (h *USBHID) Describe() Availability {
	if err := modHID.Load(); err != nil {
		return Availability{Kind: KindUSBHID, Label: "USB HID", Note: "hid.dll unavailable: " + err.Error()}
	}
	return Availability{
		Kind: KindUSBHID, Label: "USB HID", Available: true,
		Note: "Vendor HID devices such as USB thermometers. Keyboards and mice are owned by Windows and not listed.",
	}
}

func hidGUID() windows.GUID {
	var g windows.GUID
	procHidD_GetHidGuid.Call(uintptr(unsafe.Pointer(&g)))
	return g
}

func openHID(path string, overlapped bool) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	flags := uint32(0)
	if overlapped {
		flags = windows.FILE_FLAG_OVERLAPPED
	}
	return windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, flags, 0)
}

func readHIDInfo(h windows.Handle) (hidInfo, error) {
	var info hidInfo
	attrs := hiddAttributes{Size: uint32(unsafe.Sizeof(hiddAttributes{}))}
	if r, _, err := procHidD_GetAttributes.Call(uintptr(h), uintptr(unsafe.Pointer(&attrs))); r == 0 {
		return info, fmt.Errorf("HidD_GetAttributes: %w", err)
	}
	info.vidpid = fmt.Sprintf("%04X:%04X", attrs.VendorID, attrs.ProductID)

	var name [256]uint16
	if r, _, _ := procHidD_GetProductString.Call(uintptr(h), uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)*2)); r != 0 {
		info.product = windows.UTF16ToString(name[:])
	}

	var pp uintptr
	if r, _, err := procHidD_GetPreparsedData.Call(uintptr(h), uintptr(unsafe.Pointer(&pp))); r == 0 {
		return info, fmt.Errorf("HidD_GetPreparsedData: %w", err)
	}
	defer procHidD_FreePreparsedData.Call(pp)
	if r, _, _ := procHidP_GetCaps.Call(pp, uintptr(unsafe.Pointer(&info.caps))); r != hidpStatusSuccess {
		return info, fmt.Errorf("HidP_GetCaps: status %#x", r)
	}
	return info, nil
}

// Scan lists HID interfaces this process can open for read and write. That
// excludes keyboards and mice, which Windows holds exclusively, and leaves the
// vendor devices worth decoding.
func (h *USBHID) Scan(ctx context.Context) ([]DeviceInfo, error) {
	g := hidGUID()
	paths, err := windows.CM_Get_Device_Interface_List("", &g, windows.CM_GET_DEVICE_INTERFACE_LIST_PRESENT)
	if err != nil {
		return nil, fmt.Errorf("list HID interfaces: %w", err)
	}
	out := []DeviceInfo{}
	for _, path := range paths {
		hd, err := openHID(path, false)
		if err != nil {
			continue
		}
		info, err := readHIDInfo(hd)
		windows.CloseHandle(hd)
		if err != nil {
			continue
		}
		name := info.product
		if name == "" {
			name = "HID device " + info.vidpid
		}
		out = append(out, DeviceInfo{
			ID:      "usb-hid:" + strings.ToLower(path),
			Kind:    KindUSBHID,
			Name:    name,
			Address: path,
			Details: map[string]string{
				"usb":          info.vidpid,
				"inputReport":  fmt.Sprint(info.caps.InputReportByteLength),
				"outputReport": fmt.Sprint(info.caps.OutputReportByteLength),
			},
			SuggestedProfile: hidSuggest[info.vidpid],
		})
	}
	return out, nil
}

func (h *USBHID) Open(ctx context.Context, dev DeviceInfo, opts OpenOptions) (Conn, error) {
	hd, err := openHID(dev.Address, true)
	if err != nil {
		return nil, fmt.Errorf("open HID %s: %w", dev.Name, err)
	}
	info, err := readHIDInfo(hd)
	if err != nil {
		windows.CloseHandle(hd)
		return nil, err
	}
	if info.caps.InputReportByteLength == 0 {
		windows.CloseHandle(hd)
		return nil, errors.New("device has no input report")
	}
	c := &hidConn{
		dev: dev, h: hd,
		inLen: int(info.caps.InputReportByteLength), outLen: int(info.caps.OutputReportByteLength),
		frames: make(chan Frame, 64), done: make(chan struct{}), pumpDone: make(chan struct{}),
	}
	go c.pump()
	return c, nil
}

type hidConn struct {
	dev           DeviceInfo
	h             windows.Handle
	inLen, outLen int
	frames        chan Frame
	done          chan struct{}
	pumpDone      chan struct{}

	mu      sync.Mutex
	err     error
	writeMu sync.Mutex
	once    sync.Once
}

func (c *hidConn) Device() DeviceInfo   { return c.dev }
func (c *hidConn) Frames() <-chan Frame { return c.frames }

func (c *hidConn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *hidConn) fail(err error) {
	select {
	case <-c.done:
		return // closing: errors from cancelled I/O are expected
	default:
	}
	c.mu.Lock()
	if c.err == nil {
		c.err = err
	}
	c.mu.Unlock()
}

// overlappedIO runs one overlapped read or write, waking every 250 ms to
// notice Close, and gives up after timeout (0 = wait for ever).
func (c *hidConn) overlappedIO(write bool, buf []byte, timeout time.Duration) (int, error) {
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(ev)

	ov := windows.Overlapped{HEvent: ev}
	var n uint32
	if write {
		err = windows.WriteFile(c.h, buf, &n, &ov)
	} else {
		err = windows.ReadFile(c.h, buf, &n, &ov)
	}
	if err != nil && !errors.Is(err, windows.ERROR_IO_PENDING) {
		return 0, err
	}
	deadline := time.Now().Add(timeout)
	for {
		r, _ := windows.WaitForSingleObject(ev, 250)
		if r == windows.WAIT_OBJECT_0 {
			break
		}
		cancelled := false
		select {
		case <-c.done:
			cancelled = true
		default:
			cancelled = timeout > 0 && time.Now().After(deadline)
		}
		if cancelled {
			windows.CancelIoEx(c.h, &ov)
			windows.GetOverlappedResult(c.h, &ov, &n, true)
			return 0, errors.New("HID I/O cancelled")
		}
	}
	if err := windows.GetOverlappedResult(c.h, &ov, &n, false); err != nil {
		return 0, err
	}
	return int(n), nil
}

func (c *hidConn) pump() {
	defer close(c.pumpDone)
	defer close(c.frames)
	buf := make([]byte, c.inLen)
	for {
		n, err := c.overlappedIO(false, buf, 0)
		if err != nil {
			c.fail(fmt.Errorf("read HID report: %w", err))
			return
		}
		if n == 0 {
			continue
		}
		data := append([]byte(nil), buf[:n]...)
		select {
		case c.frames <- Frame{At: time.Now(), Source: "hid", Data: data}:
		case <-c.done:
			return
		}
	}
}

// Write sends one output report. p starts with the report ID (0 for devices
// without numbered reports) and is zero-padded to the report length, so a
// decoder can send []byte{0} for "an empty report" without knowing its size.
func (c *hidConn) Write(p []byte) error {
	select {
	case <-c.done:
		return errors.New("connection closed")
	default:
	}
	if c.outLen == 0 {
		return errors.New("device has no output report")
	}
	if len(p) > c.outLen {
		return fmt.Errorf("report is %d bytes; device takes %d", len(p), c.outLen)
	}
	report := make([]byte, c.outLen)
	copy(report, p)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err := c.overlappedIO(true, report, time.Second)
	return err
}

func (c *hidConn) Close() error {
	c.once.Do(func() {
		close(c.done)
		select {
		case <-c.pumpDone:
		case <-time.After(2 * time.Second):
		}
		windows.CloseHandle(c.h)
	})
	return nil
}
