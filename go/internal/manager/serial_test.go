package manager

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/expert/go-connector/internal/hub"
	"github.com/expert/go-connector/internal/model"
	"github.com/expert/go-connector/internal/transport"
)

// fakeSerial is a serial transport that replays fixed chunks and records the
// options it was opened with.
type fakeSerial struct {
	chunks [][]byte

	mu   sync.Mutex
	opts transport.OpenOptions
}

func (f *fakeSerial) Kind() transport.Kind { return transport.KindSerial }
func (f *fakeSerial) Describe() transport.Availability {
	return transport.Availability{Kind: transport.KindSerial, Available: true}
}
func (f *fakeSerial) Scan(context.Context) ([]transport.DeviceInfo, error) { return nil, nil }

func (f *fakeSerial) Open(_ context.Context, dev transport.DeviceInfo, opts transport.OpenOptions) (transport.Conn, error) {
	f.mu.Lock()
	f.opts = opts
	f.mu.Unlock()
	c := &fakeConn{dev: dev, frames: make(chan transport.Frame, len(f.chunks))}
	for _, b := range f.chunks {
		c.frames <- transport.Frame{At: time.Now(), Source: "rx", Data: b}
	}
	// The channel stays open: a device that goes quiet is the case under test.
	return c, nil
}

type fakeConn struct {
	dev    transport.DeviceInfo
	frames chan transport.Frame
	once   sync.Once
}

func (c *fakeConn) Device() transport.DeviceInfo   { return c.dev }
func (c *fakeConn) Frames() <-chan transport.Frame { return c.frames }
func (c *fakeConn) Write([]byte) error             { return nil }
func (c *fakeConn) Err() error                     { return nil }
func (c *fakeConn) Close() error                   { c.once.Do(func() { close(c.frames) }); return nil }

// An Omron HBP-9030 sends its CSV record with no terminator. The pump must
// hand the burst to the decoder once the line has been quiet, and the
// profile's line settings must reach the transport.
func TestIdleGapFlushesUnterminatedBurst(t *testing.T) {
	fs := &fakeSerial{chunks: [][]byte{
		[]byte("2021,05,25,09,42, ,0,1"),
		[]byte("14, 79,114,0"),
	}}
	h := hub.New()
	m := New(h, fs)
	t.Cleanup(m.Shutdown)
	client := h.Subscribe()

	if _, err := m.Open(context.Background(), OpenRequest{
		Transport: transport.KindSerial, Address: "COM9", Profile: "bp-omron-9030", BaudRate: 2400,
	}); err != nil {
		t.Fatalf("open: %v", err)
	}

	got := awaitMetrics(t, client,
		[]model.Metric{model.MetricBPSystolic, model.MetricBPDiastolic, model.MetricPulseRate}, 3*time.Second)
	if got[model.MetricBPSystolic].Value != 114 || got[model.MetricBPDiastolic].Value != 79 {
		t.Fatalf("got %+v", got)
	}

	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.opts.Line != nil {
		t.Fatalf("HBP-9030 is 8N1, got line %+v", fs.opts.Line)
	}
}

// The Yuwell simulator runs through the real decoder; it opens with
// finger-out packets that must not surface as readings.
func TestSimYuwellOximeterFlowsThroughVendorDecoder(t *testing.T) {
	m, client := newTestManager(t)
	if _, err := m.Scan(context.Background(), transport.KindSim); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if _, err := m.Open(context.Background(), OpenRequest{
		Transport: transport.KindSim, DeviceID: "sim:yuwell-spo2", Profile: "spo2-yuwell-ble",
	}); err != nil {
		t.Fatalf("open: %v", err)
	}

	got := awaitMetrics(t, client, []model.Metric{model.MetricSpO2, model.MetricPulseRate}, 10*time.Second)
	if v := got[model.MetricSpO2].Value; v < 93 || v > 100 {
		t.Fatalf("spo2 = %v, want the simulated 93–100", v)
	}
	if v := got[model.MetricPulseRate].Value; v < 55 || v > 95 {
		t.Fatalf("pulse = %v, want the simulated 55–95", v)
	}
}

// The glucose meter only sends after the RACP write, so a glucose reading
// arriving proves the pump performed the GATT write on open.
func TestSimGlucoseAnswersRACPWrite(t *testing.T) {
	m, client := newTestManager(t)
	if _, err := m.Scan(context.Background(), transport.KindSim); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if _, err := m.Open(context.Background(), OpenRequest{
		Transport: transport.KindSim, DeviceID: "sim:ble-glucose", Profile: "ble-glucose",
	}); err != nil {
		t.Fatalf("open: %v", err)
	}
	got := awaitMetrics(t, client, []model.Metric{model.MetricGlucose}, 5*time.Second)
	// "Report last record" returns only the newest of the stored readings.
	if r := got[model.MetricGlucose]; r.Value != 104 || r.Unit != "mg/dL" {
		t.Fatalf("glucose = %+v", r)
	}
}

func TestProfileLineSettingsReachTransport(t *testing.T) {
	fs := &fakeSerial{}
	m := New(hub.New(), fs)
	t.Cleanup(m.Shutdown)

	if _, err := m.Open(context.Background(), OpenRequest{
		Transport: transport.KindSerial, Address: "COM9", Profile: "bp-omron-9020",
	}); err != nil {
		t.Fatalf("open: %v", err)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.opts.Line == nil || fs.opts.Line.Parity != "odd" {
		t.Fatalf("Omron 9020 needs odd parity, got %+v", fs.opts.Line)
	}
}
