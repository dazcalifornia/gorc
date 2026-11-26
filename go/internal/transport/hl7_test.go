package transport

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// openHL7 starts a listener on a free loopback port and returns the session
// connection plus the address a monitor would dial.
func openHL7(t *testing.T, ack string) (*hl7Conn, string) {
	t.Helper()
	h := NewHL7("127.0.0.1:0", ack)
	devs, err := h.Scan(context.Background())
	if err != nil || len(devs) != 1 {
		t.Fatalf("scan = %v, %v", devs, err)
	}
	conn, err := h.Open(context.Background(), devs[0], OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	c := conn.(*hl7Conn)
	return c, c.ln.Addr().String()
}

func nextFrame(t *testing.T, c *hl7Conn) Frame {
	t.Helper()
	select {
	case f := <-c.Frames():
		return f
	case <-time.After(3 * time.Second):
		t.Fatal("no frame")
	}
	return Frame{}
}

func TestHL7MLLPMessageGetsStandardAck(t *testing.T) {
	c, addr := openHL7(t, HL7AckStandard)
	mon, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer mon.Close()

	msg := "\x0bMSH|^~\\&|MON|ICU|||20261126||ORU^R01|CTRL-77|P|2.6\rOBX|1|NM|SPO2||97|\r\x1c\r"
	// Split the write: framing must not depend on one TCP read per message.
	mon.Write([]byte(msg[:20]))
	time.Sleep(20 * time.Millisecond)
	mon.Write([]byte(msg[20:]))

	f := nextFrame(t, c)
	if !strings.Contains(string(f.Data), "OBX|1|NM|SPO2||97|") || !strings.HasPrefix(f.Source, "tcp:127.0.0.1:") {
		t.Fatalf("frame = %q from %s", f.Data, f.Source)
	}

	mon.SetReadDeadline(time.Now().Add(2 * time.Second))
	ack, err := bufio.NewReader(mon).ReadString('\x1c')
	if err != nil {
		t.Fatalf("no ack: %v", err)
	}
	if !strings.HasPrefix(ack, "\x0bMSH|") || !strings.Contains(ack, "\rMSA|AA|CTRL-77\r") || !strings.Contains(ack, "|P|2.6\r") {
		t.Fatalf("ack = %q", ack)
	}
}

func TestHL7UnframedMessageEndsOnSilenceWithLegacyAck(t *testing.T) {
	c, addr := openHL7(t, HL7AckLegacy)
	mon, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer mon.Close()

	mon.Write([]byte("MSH|^~\\&|IM70\r\nOBX|1|NM|NIBP_SYS||118|\r\n"))
	f := nextFrame(t, c)
	if !strings.Contains(string(f.Data), "NIBP_SYS||118") {
		t.Fatalf("frame = %q", f.Data)
	}

	buf := make([]byte, 256)
	mon.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := mon.Read(buf)
	if err != nil || string(buf[:n]) != hl7LegacyAck {
		t.Fatalf("ack = %q, %v", buf[:n], err)
	}
}

func TestHL7CloseStopsListening(t *testing.T) {
	c, addr := openHL7(t, HL7AckNone)
	c.Close()
	for range c.Frames() {
	}
	if mon, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
		mon.Close()
		t.Fatal("listener still accepting after Close")
	}
}
