package manager

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/expert/go-connector/internal/hub"
	"github.com/expert/go-connector/internal/model"
	"github.com/expert/go-connector/internal/transport"
)

// newTestManager wires the simulator through the real decoders and a real hub,
// so these tests cover the production path end to end.
func newTestManager(t *testing.T) (*Manager, *hub.Client) {
	t.Helper()
	h := hub.New()
	m := New(h, transport.NewSim())
	t.Cleanup(m.Shutdown)
	return m, h.Subscribe()
}

// awaitMetrics collects readings from the websocket stream until every wanted
// metric has arrived or the deadline passes.
func awaitMetrics(t *testing.T, c *hub.Client, want []model.Metric, timeout time.Duration) map[model.Metric]model.Reading {
	t.Helper()
	got := map[model.Metric]model.Reading{}
	deadline := time.After(timeout)

	for {
		missing := false
		for _, w := range want {
			if _, ok := got[w]; !ok {
				missing = true
			}
		}
		if !missing {
			return got
		}

		select {
		case payload := <-c.Send():
			var ev Event
			if err := json.Unmarshal(payload, &ev); err != nil {
				t.Fatalf("unmarshal event: %v", err)
			}
			for _, r := range ev.Readings {
				got[r.Metric] = r
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %v; got %v", want, keys(got))
		}
	}
}

func keys(m map[model.Metric]model.Reading) []model.Metric {
	out := make([]model.Metric, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestSimOximeterFlowsThroughBCIDecoder(t *testing.T) {
	m, client := newTestManager(t)

	if _, err := m.Scan(context.Background(), transport.KindSim); err != nil {
		t.Fatalf("scan: %v", err)
	}
	sess, err := m.Open(context.Background(), OpenRequest{
		Transport: transport.KindSim, DeviceID: "sim:spo2", Profile: "spo2-bci",
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if sess.State != StateLive {
		t.Fatalf("state = %q, want live", sess.State)
	}

	// The simulator spends its first three seconds searching for a finger, so
	// real SpO2 values need longer than the waveform does.
	got := awaitMetrics(t, client, []model.Metric{
		model.MetricPleth, model.MetricSpO2, model.MetricPulseRate,
	}, 12*time.Second)

	if v := got[model.MetricSpO2].Value; v < 90 || v > 100 {
		t.Fatalf("SpO2 = %v, outside plausible range", v)
	}
	if v := got[model.MetricPulseRate].Value; v < 40 || v > 200 {
		t.Fatalf("pulse = %v, outside plausible range", v)
	}
	if !got[model.MetricPleth].Waveform {
		t.Fatal("pleth not flagged as waveform")
	}
	if got[model.MetricSpO2].SessionID != sess.ID {
		t.Fatal("reading not stamped with its session id")
	}

	// Latest must hold scalars but never waveform samples.
	view, ok := m.Get(sess.ID)
	if !ok {
		t.Fatal("session disappeared")
	}
	if view.Frames == 0 || view.Readings == 0 {
		t.Fatalf("counters not advancing: %+v", view)
	}
	for _, r := range view.Latest {
		if r.Metric == model.MetricPleth {
			t.Fatal("waveform sample leaked into latest values")
		}
	}
}

func TestSimBLEScaleFlowsThroughGATTDecoder(t *testing.T) {
	m, client := newTestManager(t)
	if _, err := m.Scan(context.Background(), transport.KindSim); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if _, err := m.Open(context.Background(), OpenRequest{
		Transport: transport.KindSim, DeviceID: "sim:ble-scale", Profile: "ble-gatt",
	}); err != nil {
		t.Fatalf("open: %v", err)
	}

	got := awaitMetrics(t, client, []model.Metric{
		model.MetricWeight, model.MetricBMI, model.MetricHeight, model.MetricBodyFat,
	}, 10*time.Second)

	if v := got[model.MetricWeight].Value; v < 40 || v > 150 {
		t.Fatalf("weight = %v kg, implausible", v)
	}
	if u := got[model.MetricWeight].Unit; u != "kg" {
		t.Fatalf("weight unit = %q", u)
	}
	if v := got[model.MetricHeight].Value; v < 120 || v > 220 {
		t.Fatalf("height = %v cm, implausible (unit scaling bug?)", v)
	}
	if v := got[model.MetricBMI].Value; v < 10 || v > 60 {
		t.Fatalf("BMI = %v, implausible", v)
	}
}

func TestSimVitalsProducesTemperatureAndBloodPressure(t *testing.T) {
	m, client := newTestManager(t)
	if _, err := m.Scan(context.Background(), transport.KindSim); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if _, err := m.Open(context.Background(), OpenRequest{
		Transport: transport.KindSim, DeviceID: "sim:ble-vitals", Profile: "ble-gatt",
	}); err != nil {
		t.Fatalf("open: %v", err)
	}

	got := awaitMetrics(t, client, []model.Metric{
		model.MetricTemperature, model.MetricBPSystolic, model.MetricBPDiastolic,
	}, 15*time.Second)

	if v := got[model.MetricTemperature].Value; v < 34 || v > 42 {
		t.Fatalf("temperature = %v, implausible", v)
	}
	if sys, dia := got[model.MetricBPSystolic].Value, got[model.MetricBPDiastolic].Value; sys <= dia {
		t.Fatalf("systolic %v not above diastolic %v", sys, dia)
	}
}

func TestSerialScaleSimulatorParsesASCII(t *testing.T) {
	m, client := newTestManager(t)
	if _, err := m.Scan(context.Background(), transport.KindSim); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if _, err := m.Open(context.Background(), OpenRequest{
		Transport: transport.KindSim, DeviceID: "sim:scale", Profile: "ascii-lines", BaudRate: 9600,
	}); err != nil {
		t.Fatalf("open: %v", err)
	}
	got := awaitMetrics(t, client, []model.Metric{model.MetricWeight}, 10*time.Second)
	if v := got[model.MetricWeight].Value; v < 0 || v > 200 {
		t.Fatalf("weight = %v, implausible", v)
	}
}

func TestCloseEndsSessionAndStopsReadings(t *testing.T) {
	m, _ := newTestManager(t)
	if _, err := m.Scan(context.Background(), transport.KindSim); err != nil {
		t.Fatalf("scan: %v", err)
	}
	sess, err := m.Open(context.Background(), OpenRequest{
		Transport: transport.KindSim, DeviceID: "sim:ble-oximeter", Profile: "ble-gatt",
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := m.Close(sess.ID); err != nil {
		t.Fatalf("close: %v", err)
	}

	view, _ := m.Get(sess.ID)
	if view.State != StateClosed {
		t.Fatalf("state = %q, want closed", view.State)
	}
	if view.EndedAt == nil {
		t.Fatal("endedAt not set")
	}
	if n := m.Forget(); n != 1 {
		t.Fatalf("Forget removed %d sessions, want 1", n)
	}
	if _, ok := m.Get(sess.ID); ok {
		t.Fatal("session still present after Forget")
	}
}

func TestOpenRejectsUnknownProfileAndDevice(t *testing.T) {
	m, _ := newTestManager(t)
	if _, err := m.Open(context.Background(), OpenRequest{
		Transport: transport.KindSim, DeviceID: "sim:spo2", Profile: "no-such-profile",
	}); err == nil {
		t.Fatal("expected an error for an unknown profile")
	}
	// Connecting without a prior scan and without an address must fail
	// explicitly rather than silently inventing a device.
	if _, err := m.Open(context.Background(), OpenRequest{
		Transport: transport.KindSim, DeviceID: "sim:nope", Profile: "ble-gatt",
	}); err == nil {
		t.Fatal("expected an error for an unknown device")
	}
}

func TestTransportsAreReportedInStableOrder(t *testing.T) {
	h := hub.New()
	m := New(h, transport.NewBTClassic(), transport.NewBLE(), transport.NewSerial(), transport.NewSim())
	kinds := []transport.Kind{}
	for _, a := range m.Transports() {
		kinds = append(kinds, a.Kind)
	}
	want := []transport.Kind{transport.KindSim, transport.KindSerial, transport.KindBLE, transport.KindBTClassic}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("order = %v, want %v", kinds, want)
		}
	}
}
