package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/expert/go-connector/internal/hub"
	"github.com/expert/go-connector/internal/manager"
	"github.com/expert/go-connector/internal/transport"
)

func newTestApp(t *testing.T) *fiber.App {
	t.Helper()
	h := hub.New()
	m := manager.New(h, transport.NewSim())
	t.Cleanup(m.Shutdown)
	app := fiber.New()
	New(m, h).Register(app)
	return app
}

func do(t *testing.T, app *fiber.App, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var r *http.Request
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	resp, err := app.Test(r, 20000)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	return resp, buf.Bytes()
}

func TestTransportsAndProfilesAreDiscoverable(t *testing.T) {
	app := newTestApp(t)

	resp, body := do(t, app, http.MethodGet, "/api/transports", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	var avail []transport.Availability
	if err := json.Unmarshal(body, &avail); err != nil || len(avail) == 0 {
		t.Fatalf("transports: %v %s", err, body)
	}

	_, body = do(t, app, http.MethodGet, "/api/profiles", nil)
	var profiles []struct{ ID string }
	if err := json.Unmarshal(body, &profiles); err != nil {
		t.Fatalf("profiles: %v %s", err, body)
	}
	found := map[string]bool{}
	for _, p := range profiles {
		found[p.ID] = true
	}
	for _, want := range []string{"spo2-bci", "ascii-lines", "ble-gatt"} {
		if !found[want] {
			t.Fatalf("profile %q missing from %s", want, body)
		}
	}
}

func TestScanRequiresTransportAndRejectsUnknown(t *testing.T) {
	app := newTestApp(t)

	if resp, _ := do(t, app, http.MethodGet, "/api/scan", nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing transport: status %d, want 400", resp.StatusCode)
	}
	if resp, _ := do(t, app, http.MethodGet, "/api/scan?transport=telepathy", nil); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("unknown transport: status %d, want 502", resp.StatusCode)
	}
}

func TestSessionLifecycleOverREST(t *testing.T) {
	app := newTestApp(t)

	resp, body := do(t, app, http.MethodGet, "/api/scan?transport=sim", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("scan: status %d: %s", resp.StatusCode, body)
	}
	var scan struct {
		Devices []transport.DeviceInfo `json:"devices"`
	}
	if err := json.Unmarshal(body, &scan); err != nil || len(scan.Devices) == 0 {
		t.Fatalf("scan body: %v %s", err, body)
	}
	// The simulator must tell the UI which decoder it needs.
	if scan.Devices[0].SuggestedProfile == "" {
		t.Fatalf("no suggested profile on %+v", scan.Devices[0])
	}

	resp, body = do(t, app, http.MethodPost, "/api/sessions", manager.OpenRequest{
		Transport: transport.KindSim, DeviceID: "sim:ble-oximeter", Profile: "ble-gatt",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("open: status %d: %s", resp.StatusCode, body)
	}
	var sess manager.Session
	if err := json.Unmarshal(body, &sess); err != nil {
		t.Fatalf("session body: %v %s", err, body)
	}

	if resp, body = do(t, app, http.MethodGet, "/api/sessions/"+sess.ID, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("get session: status %d: %s", resp.StatusCode, body)
	}

	// A hex write must be accepted and decoded; the simulator swallows it.
	if resp, body = do(t, app, http.MethodPost, "/api/sessions/"+sess.ID+"/write",
		writeRequest{Hex: "7D 81 A1"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("write: status %d: %s", resp.StatusCode, body)
	}
	var wrote struct {
		Written int `json:"written"`
	}
	if json.Unmarshal(body, &wrote); wrote.Written != 3 {
		t.Fatalf("wrote %d bytes, want 3 (%s)", wrote.Written, body)
	}
	if resp, body = do(t, app, http.MethodPost, "/api/sessions/"+sess.ID+"/write",
		writeRequest{Hex: "nothex"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad hex: status %d: %s", resp.StatusCode, body)
	}

	if resp, _ = do(t, app, http.MethodDelete, "/api/sessions/"+sess.ID, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("close: status %d", resp.StatusCode)
	}
	if resp, _ = do(t, app, http.MethodDelete, "/api/sessions/does-not-exist", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("close unknown: status %d, want 404", resp.StatusCode)
	}
}

func TestOpenRejectsBadRequests(t *testing.T) {
	app := newTestApp(t)
	// No profile.
	if resp, _ := do(t, app, http.MethodPost, "/api/sessions", manager.OpenRequest{
		Transport: transport.KindSim, DeviceID: "sim:spo2",
	}); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", resp.StatusCode)
	}
}

// A plain GET on the websocket route must say what is wrong rather than
// failing obscurely.
func TestWebsocketRouteRequiresUpgrade(t *testing.T) {
	app := newTestApp(t)
	resp, _ := do(t, app, http.MethodGet, "/ws", nil)
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Fatalf("status %d, want 426", resp.StatusCode)
	}
}

func TestHealth(t *testing.T) {
	app := newTestApp(t)
	resp, body := do(t, app, http.MethodGet, "/api/health", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	var h struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(body, &h); h.Status != "ok" {
		t.Fatalf("health = %s", body)
	}
}
