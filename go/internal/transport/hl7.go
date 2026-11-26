package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// KindHL7 is a TCP listener for bedside monitors that push HL7 results.
const KindHL7 Kind = "hl7"

// HL7 ACK styles. The SMART EASY OPD kiosk sent one fixed, malformed ACK
// (no MLLP, no segment separator, fixed control id); monitors in the field
// accepted it, which suggests they ignore ACK content. "standard" is a real
// MLLP-wrapped ACK echoing the message's control id.
const (
	HL7AckStandard = "standard"
	HL7AckLegacy   = "legacy"
	HL7AckNone     = "none"
)

const (
	hl7MaxClients  = 8 // OPD MAX_HL7_CLIENTS; one is normal
	hl7IdleGap     = 100 * time.Millisecond
	hl7IdleTimeout = 300 * time.Second // OPD HL7_IDLE_TIMEOUT_SEC
	hl7MaxMessage  = 1 << 20
	mllpStart      = 0x0B
	mllpEnd        = 0x1C
	hl7LegacyAck   = `MSH|^~\&|LABADT|DH|EPICADT|DH|||ACK^A01^ACK|HL7ACK00001|P|2.3|MSA|AA|HL7MSG00001`
)

// HL7 listens for monitors only while a session is open on it: the monitor
// is the TCP client, as in the OPD kiosk.
type HL7 struct {
	addr string
	ack  string
}

// NewHL7 returns an HL7 listener transport. An empty addr disables it.
func NewHL7(addr, ack string) *HL7 {
	switch ack {
	case HL7AckLegacy, HL7AckNone:
	default:
		ack = HL7AckStandard
	}
	return &HL7{addr: addr, ack: ack}
}

func (h *HL7) Kind() Kind { return KindHL7 }

func (h *HL7) Describe() Availability {
	if h.addr == "" {
		return Availability{Kind: KindHL7, Label: "HL7 monitor (TCP)", Note: "Disabled: start the server with -hl7 <addr> to listen for bedside monitors."}
	}
	return Availability{
		Kind: KindHL7, Label: "HL7 monitor (TCP)", Available: true,
		Note: fmt.Sprintf("Listens on %s for bedside monitors that push HL7 results (the OPD kiosk used port 1901); "+
			"point the monitor's HL7 server setting at this machine. Up to %d monitors share one session. ACK style: %s.",
			h.addr, hl7MaxClients, h.ack),
	}
}

func (h *HL7) Scan(ctx context.Context) ([]DeviceInfo, error) {
	if h.addr == "" {
		return []DeviceInfo{}, nil
	}
	return []DeviceInfo{{
		ID:               "hl7:" + h.addr,
		Kind:             KindHL7,
		Name:             "HL7 listener " + h.addr,
		Address:          h.addr,
		Details:          map[string]string{"ack": h.ack},
		SuggestedProfile: "hl7-obx",
	}}, nil
}

func (h *HL7) Open(ctx context.Context, dev DeviceInfo, opts OpenOptions) (Conn, error) {
	ln, err := net.Listen("tcp", dev.Address)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", dev.Address, err)
	}
	c := &hl7Conn{
		dev: dev, ack: h.ack, ln: ln,
		frames:  make(chan Frame, 64),
		done:    make(chan struct{}),
		clients: map[int64]net.Conn{},
	}
	go c.accept()
	return c, nil
}

type hl7Conn struct {
	dev    DeviceInfo
	ack    string
	ln     net.Listener
	frames chan Frame
	done   chan struct{}

	mu      sync.Mutex
	clients map[int64]net.Conn
	nextID  int64
	err     error
	wg      sync.WaitGroup
	once    sync.Once
}

func (c *hl7Conn) Device() DeviceInfo   { return c.dev }
func (c *hl7Conn) Frames() <-chan Frame { return c.frames }

func (c *hl7Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *hl7Conn) accept() {
	defer func() {
		c.wg.Wait()
		close(c.frames)
	}()
	for {
		nc, err := c.ln.Accept()
		if err != nil {
			select {
			case <-c.done:
			default:
				c.mu.Lock()
				c.err = err
				c.mu.Unlock()
			}
			return
		}
		c.mu.Lock()
		if len(c.clients) >= hl7MaxClients {
			// Evict the oldest: a half-open connection from a monitor that
			// rebooted must not lock the next one out (OPD EvictOldestHl7Client).
			oldest := int64(-1)
			for id := range c.clients {
				if oldest < 0 || id < oldest {
					oldest = id
				}
			}
			c.clients[oldest].Close()
			delete(c.clients, oldest)
		}
		c.nextID++
		id := c.nextID
		c.clients[id] = nc
		c.wg.Add(1)
		c.mu.Unlock()
		go c.serve(id, nc)
	}
}

// serve reads one monitor. A message ends at the MLLP end byte when the
// monitor frames, otherwise after a quiet gap, as the OPD kiosk did.
func (c *hl7Conn) serve(id int64, nc net.Conn) {
	defer c.wg.Done()
	defer func() {
		c.mu.Lock()
		delete(c.clients, id)
		c.mu.Unlock()
		nc.Close()
	}()

	src := "tcp:" + nc.RemoteAddr().String()
	var buf []byte
	chunk := make([]byte, 4096)
	lastData := time.Now()

	deliver := func(msg []byte) bool {
		msg = bytes.ReplaceAll(msg, []byte{0}, nil)
		if len(bytes.TrimSpace(bytes.Trim(msg, "\x0b\x1c"))) == 0 {
			return true
		}
		select {
		case c.frames <- Frame{At: time.Now(), Source: src, Data: msg}:
		case <-c.done:
			return false
		}
		if ack := c.ackFor(msg); ack != nil {
			nc.SetWriteDeadline(time.Now().Add(2 * time.Second))
			nc.Write(ack)
		}
		return true
	}

	for {
		select {
		case <-c.done:
			return
		default:
		}
		nc.SetReadDeadline(time.Now().Add(hl7IdleGap))
		n, err := nc.Read(chunk)
		if n > 0 {
			lastData = time.Now()
			buf = append(buf, chunk[:n]...)
			for {
				end := bytes.IndexByte(buf, mllpEnd)
				if end < 0 {
					break
				}
				msg := buf[:end+1]
				buf = append([]byte(nil), buf[end+1:]...)
				if !deliver(msg) {
					return
				}
			}
			if len(buf) > hl7MaxMessage {
				buf = buf[:0] // runaway: no terminator and no pause
			}
		}
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				if len(bytes.TrimSpace(buf)) > 0 && !bytes.Contains(buf, []byte{mllpStart}) {
					// Unframed monitor: silence ends the message.
					if !deliver(buf) {
						return
					}
					buf = nil
				}
				if time.Since(lastData) > hl7IdleTimeout {
					return
				}
				continue
			}
			if len(bytes.TrimSpace(buf)) > 0 {
				deliver(buf)
			}
			return // monitor hung up
		}
	}
}

// ackFor builds the reply for one received message.
func (c *hl7Conn) ackFor(msg []byte) []byte {
	switch c.ack {
	case HL7AckNone:
		return nil
	case HL7AckLegacy:
		return []byte(hl7LegacyAck)
	}
	ctrlID, version := "", "2.3"
	for _, seg := range strings.FieldsFunc(string(msg), func(r rune) bool { return r == '\r' || r == '\n' }) {
		seg = strings.Trim(seg, "\x0b\x1c ")
		if !strings.HasPrefix(seg, "MSH") {
			continue
		}
		f := strings.Split(seg, "|")
		if len(f) > 9 {
			ctrlID = f[9]
		}
		if len(f) > 11 && f[11] != "" {
			version = f[11]
		}
		break
	}
	now := time.Now().Format("20060102150405")
	body := fmt.Sprintf("MSH|^~\\&|GORC|GORC|||%s||ACK^R01^ACK|%s|P|%s\rMSA|AA|%s\r", now, "ACK"+now, version, ctrlID)
	return append(append([]byte{mllpStart}, body...), mllpEnd, '\r')
}

// Write sends bytes to every connected monitor.
func (c *hl7Conn) Write(p []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.clients) == 0 {
		return errors.New("no monitor is connected")
	}
	var lastErr error
	for _, nc := range c.clients {
		nc.SetWriteDeadline(time.Now().Add(2 * time.Second))
		if _, err := nc.Write(p); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

func (c *hl7Conn) Close() error {
	c.once.Do(func() {
		close(c.done)
		c.ln.Close()
		c.mu.Lock()
		for _, nc := range c.clients {
			nc.Close()
		}
		c.mu.Unlock()
	})
	return nil
}
