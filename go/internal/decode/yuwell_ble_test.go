package decode

import (
	"testing"
	"time"

	"github.com/expert/go-connector/internal/model"
	"github.com/expert/go-connector/internal/transport"
)

const (
	uuidFFE4 = "0000ffe4-0000-1000-8000-00805f9b34fb"
	uuidFFE1 = "0000ffe1-0000-1000-8000-00805f9b34fb"
)

func yuwell(src string, at time.Time, b ...byte) transport.Frame {
	return transport.Frame{At: at, Source: src, Data: b}
}

func TestYuwellReadsPulseByte4AndSpO2Byte5(t *testing.T) {
	d := newDecoder(t, "spo2-yuwell-ble")
	rs := d.Feed(yuwell(uuidFFE4, at, 0x00, 0x00, 0x00, 0x00, 72, 98))
	near(t, pick(t, rs, model.MetricSpO2).Value, 98)
	near(t, pick(t, rs, model.MetricPulseRate).Value, 72)
}

func TestYuwellAcceptsLegacyFFE1(t *testing.T) {
	rs := newDecoder(t, "spo2-yuwell-ble").Feed(yuwell(uuidFFE1, at, 1, 2, 3, 4, 66, 96, 7))
	near(t, pick(t, rs, model.MetricSpO2).Value, 96)
}

func TestYuwellIgnoresOtherCharacteristicsAndShortPackets(t *testing.T) {
	d := newDecoder(t, "spo2-yuwell-ble")
	// Battery level, and a five-byte packet the OPD code also skipped.
	for _, f := range []transport.Frame{
		yuwell("00002a19-0000-1000-8000-00805f9b34fb", at, 0, 0, 0, 0, 72, 98),
		yuwell(uuidFFE4, at, 0, 0, 0, 0, 72),
	} {
		if rs := d.Feed(f); len(rs) != 0 {
			t.Fatalf("%s % X produced %v", f.Source, f.Data, rs)
		}
	}
}

func TestYuwellDropsFingerOutAndInvalidValues(t *testing.T) {
	d := newDecoder(t, "spo2-yuwell-ble")
	for _, b := range [][]byte{
		{0, 0, 0, 0, 0, 0},     // no finger
		{0, 0, 0, 0, 255, 127}, // "no reading" markers
	} {
		if rs := d.Feed(yuwell(uuidFFE4, at, b...)); len(rs) != 0 {
			t.Fatalf("% X produced %v", b, rs)
		}
	}
	// One valid field is still reported on its own.
	rs := d.Feed(yuwell(uuidFFE4, at, 0, 0, 0, 0, 0, 97))
	near(t, pick(t, rs, model.MetricSpO2).Value, 97)
	if has(rs, model.MetricPulseRate) {
		t.Fatal("pulse 0 should not be reported")
	}
}

func TestYuwellThrottlesUnchangedStream(t *testing.T) {
	d := newDecoder(t, "spo2-yuwell-ble")
	p := []byte{0, 0, 0, 0, 72, 98}
	if rs := d.Feed(yuwell(uuidFFE4, at, p...)); len(rs) == 0 {
		t.Fatal("first packet should report")
	}
	if rs := d.Feed(yuwell(uuidFFE4, at.Add(200*time.Millisecond), p...)); len(rs) != 0 {
		t.Fatalf("unchanged packet inside 1 s reported %v", rs)
	}
	if rs := d.Feed(yuwell(uuidFFE4, at.Add(1100*time.Millisecond), p...)); len(rs) == 0 {
		t.Fatal("heartbeat after 1 s should report")
	}
	if rs := d.Feed(yuwell(uuidFFE4, at.Add(1200*time.Millisecond), 0, 0, 0, 0, 73, 98)); len(rs) == 0 {
		t.Fatal("a changed value should report immediately")
	}
}
