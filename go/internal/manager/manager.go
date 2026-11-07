// Package manager owns device sessions: it opens a transport, drives the
// decoder, keeps the latest value per metric and publishes everything to the
// websocket hub. It is the only place that knows about all three layers.
package manager

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/expert/go-connector/internal/decode"
	"github.com/expert/go-connector/internal/hub"
	"github.com/expert/go-connector/internal/model"
	"github.com/expert/go-connector/internal/transport"
)

const (
	// flushInterval coalesces readings before they hit the socket. A 60 Hz
	// waveform is 60 messages a second per client otherwise; batching turns
	// that into 20 messages carrying 3 samples each.
	flushInterval = 50 * time.Millisecond
	// maxBatch bounds a single websocket message.
	maxBatch = 128
	// rawLogRate caps how many raw-frame log lines a session publishes per
	// second. The log is a debugging aid, not a data path.
	rawLogRate = 8
	// stateInterval throttles session-state broadcasts. Counters and latest
	// values only need to look live, not match the waveform rate.
	stateInterval = 500 * time.Millisecond
)

// SessionState is the lifecycle of one device session.
type SessionState string

const (
	StateConnecting SessionState = "connecting"
	StateLive       SessionState = "live"
	StateClosed     SessionState = "closed"
	StateError      SessionState = "error"
)

// Session is the API/websocket view of a session.
type Session struct {
	ID        string               `json:"id"`
	Device    transport.DeviceInfo `json:"device"`
	Profile   string               `json:"profile"`
	BaudRate  int                  `json:"baudRate,omitempty"`
	State     SessionState         `json:"state"`
	Error     string               `json:"error,omitempty"`
	StartedAt time.Time            `json:"startedAt"`
	EndedAt   *time.Time           `json:"endedAt,omitempty"`
	Frames    int64                `json:"frames"`
	Bytes     int64                `json:"bytes"`
	Readings  int64                `json:"readingCount"`
	// Latest is the most recent value per metric, excluding waveform samples.
	Latest []model.Reading `json:"latest"`
}

// LogLine is a human-readable session event, including raw traffic.
type LogLine struct {
	SessionID string    `json:"sessionId"`
	At        time.Time `json:"at"`
	Level     string    `json:"level"` // info | warn | error | rx | tx
	Message   string    `json:"message"`
	Hex       string    `json:"hex,omitempty"`
}

// Event is the websocket envelope.
type Event struct {
	Type     string          `json:"type"` // snapshot | session | readings | log
	At       time.Time       `json:"at"`
	Sessions []Session       `json:"sessions,omitempty"`
	Session  *Session        `json:"session,omitempty"`
	Readings []model.Reading `json:"readings,omitempty"`
	Log      *LogLine        `json:"log,omitempty"`
}

// OpenRequest is the body of POST /api/sessions.
type OpenRequest struct {
	Transport transport.Kind `json:"transport"`
	DeviceID  string         `json:"deviceId"`
	// Address lets a caller connect to a device it already knows about without
	// scanning first — a named serial port, for instance.
	Address  string `json:"address,omitempty"`
	Profile  string `json:"profile"`
	BaudRate int    `json:"baudRate,omitempty"`
}

// Manager holds the transports and the live sessions.
type Manager struct {
	hub        *hub.Hub
	transports map[transport.Kind]transport.Transport

	mu       sync.RWMutex
	sessions map[string]*session
	// discovered caches the last scan per transport so a connect request can
	// be made with just a device id.
	discovered map[transport.Kind]map[string]transport.DeviceInfo

	nextID atomic.Uint64
}

func New(h *hub.Hub, ts ...transport.Transport) *Manager {
	m := &Manager{
		hub:        h,
		transports: map[transport.Kind]transport.Transport{},
		sessions:   map[string]*session{},
		discovered: map[transport.Kind]map[string]transport.DeviceInfo{},
	}
	for _, t := range ts {
		m.transports[t.Kind()] = t
	}
	return m
}

// Transports reports what this host can do, for the UI's transport picker.
func (m *Manager) Transports() []transport.Availability {
	out := make([]transport.Availability, 0, len(m.transports))
	for _, t := range m.transports {
		out = append(out, t.Describe())
	}
	// Fixed order so the UI tabs do not jump around between requests.
	order := map[transport.Kind]int{
		transport.KindSim: 0, transport.KindSerial: 1,
		transport.KindBLE: 2, transport.KindBTClassic: 3,
	}
	sort.Slice(out, func(i, j int) bool { return order[out[i].Kind] < order[out[j].Kind] })
	return out
}

// Scan discovers devices on one transport and remembers the result.
func (m *Manager) Scan(ctx context.Context, kind transport.Kind) ([]transport.DeviceInfo, error) {
	t, ok := m.transports[kind]
	if !ok {
		return nil, fmt.Errorf("unknown transport %q", kind)
	}
	if a := t.Describe(); !a.Available {
		return nil, fmt.Errorf("%s is unavailable: %s", a.Label, a.Note)
	}

	devs, err := t.Scan(ctx)
	if err != nil {
		return nil, err
	}
	index := make(map[string]transport.DeviceInfo, len(devs))
	for _, d := range devs {
		index[d.ID] = d
	}
	m.mu.Lock()
	m.discovered[kind] = index
	m.mu.Unlock()
	return devs, nil
}

// Open connects to a device and starts decoding.
func (m *Manager) Open(ctx context.Context, req OpenRequest) (Session, error) {
	t, ok := m.transports[req.Transport]
	if !ok {
		return Session{}, fmt.Errorf("unknown transport %q", req.Transport)
	}
	if req.Profile == "" {
		return Session{}, errors.New("a decoder profile is required")
	}
	dec, err := decode.New(req.Profile)
	if err != nil {
		return Session{}, err
	}

	dev, err := m.resolve(req)
	if err != nil {
		return Session{}, err
	}

	opts := transport.OpenOptions{BaudRate: req.BaudRate, Profile: req.Profile}
	conn, err := t.Open(ctx, dev, opts)
	if err != nil {
		return Session{}, err
	}

	s := &session{
		id:        fmt.Sprintf("s%d", m.nextID.Add(1)),
		device:    conn.Device(),
		profile:   req.Profile,
		baud:      req.BaudRate,
		state:     StateLive,
		startedAt: time.Now(),
		latest:    map[model.Metric]model.Reading{},
		conn:      conn,
		dec:       dec,
		mgr:       m,
		done:      make(chan struct{}),
	}

	m.mu.Lock()
	m.sessions[s.id] = s
	m.mu.Unlock()

	go s.pump()

	view := s.view()
	m.hub.Broadcast(Event{Type: "session", At: time.Now(), Session: &view})
	s.log("info", fmt.Sprintf("connected to %s over %s using profile %s", dev.Name, dev.Kind, req.Profile), nil)
	return view, nil
}

// resolve turns an Open request into a DeviceInfo, preferring the cached scan
// result (which carries platform handles the transport needs) and falling back
// to a caller-supplied address so a known serial port can be opened directly.
func (m *Manager) resolve(req OpenRequest) (transport.DeviceInfo, error) {
	m.mu.RLock()
	cached, ok := m.discovered[req.Transport][req.DeviceID]
	m.mu.RUnlock()
	if ok {
		return cached, nil
	}
	if req.Address != "" {
		return transport.DeviceInfo{
			ID:      string(req.Transport) + ":" + req.Address,
			Kind:    req.Transport,
			Name:    req.Address,
			Address: req.Address,
			Details: map[string]string{"source": "supplied by client"},
		}, nil
	}
	// BLE in particular cannot connect without a prior scan, because the
	// platform address handle is only valid within this process.
	return transport.DeviceInfo{}, fmt.Errorf("device %q not found; scan %s first, or supply an address", req.DeviceID, req.Transport)
}

// Sessions returns every session, newest first.
func (m *Manager) Sessions() []Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s.view())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// Get returns one session.
func (m *Manager) Get(id string) (Session, bool) {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return Session{}, false
	}
	return s.view(), true
}

// Close disconnects a session.
func (m *Manager) Close(id string) error {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("no session %q", id)
	}
	s.shutdown(nil)
	return nil
}

// Write sends bytes to a device, which is how you poke a device whose wake-up
// sequence you are still working out.
func (m *Manager) Write(id string, payload []byte) error {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("no session %q", id)
	}
	if err := s.conn.Write(payload); err != nil {
		return err
	}
	s.log("tx", fmt.Sprintf("wrote %d bytes", len(payload)), payload)
	return nil
}

// Forget drops closed sessions from the list.
func (m *Manager) Forget() int {
	m.mu.Lock()
	removed := 0
	for id, s := range m.sessions {
		if st := s.view().State; st == StateClosed || st == StateError {
			delete(m.sessions, id)
			removed++
		}
	}
	m.mu.Unlock()
	if removed > 0 {
		m.hub.Broadcast(m.Snapshot())
	}
	return removed
}

// Snapshot is the first message a new websocket client receives so it renders
// current state immediately instead of waiting for the next reading.
func (m *Manager) Snapshot() Event {
	return Event{Type: "snapshot", At: time.Now(), Sessions: m.Sessions()}
}

// Shutdown closes every session, for graceful server exit.
func (m *Manager) Shutdown() {
	m.mu.RLock()
	all := make([]*session, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.mu.RUnlock()
	for _, s := range all {
		s.shutdown(nil)
	}
}

// --- session --------------------------------------------------------------

type session struct {
	id        string
	device    transport.DeviceInfo
	profile   string
	baud      int
	startedAt time.Time

	conn transport.Conn
	dec  decode.Decoder
	mgr  *Manager

	mu       sync.Mutex
	state    SessionState
	errMsg   string
	endedAt  *time.Time
	frames   int64
	bytes    int64
	readings int64
	latest   map[model.Metric]model.Reading

	done      chan struct{}
	closeOnce sync.Once
}

func (s *session) view() Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	latest := make([]model.Reading, 0, len(s.latest))
	for _, r := range s.latest {
		latest = append(latest, r)
	}
	sort.Slice(latest, func(i, j int) bool { return latest[i].Metric < latest[j].Metric })

	return Session{
		ID: s.id, Device: s.device, Profile: s.profile, BaudRate: s.baud,
		State: s.state, Error: s.errMsg, StartedAt: s.startedAt, EndedAt: s.endedAt,
		Frames: s.frames, Bytes: s.bytes, Readings: s.readings, Latest: latest,
	}
}

func (s *session) log(level, msg string, raw []byte) {
	line := LogLine{SessionID: s.id, At: time.Now(), Level: level, Message: msg}
	if len(raw) > 0 {
		line.Hex = hex.EncodeToString(raw)
	}
	s.mgr.hub.Broadcast(Event{Type: "log", At: line.At, Log: &line})
}

// pump is the heart of a session: read frames, decode, batch, publish.
func (s *session) pump() {
	defer s.shutdown(s.conn.Err())

	// Some devices need telling to start, and telling again to keep going.
	if p, ok := s.dec.(decode.Primer); ok {
		for _, cmd := range p.OnOpen() {
			if err := s.conn.Write(cmd); err != nil {
				s.log("warn", fmt.Sprintf("start command failed: %v", err), cmd)
				break
			}
			s.log("tx", "sent start command", cmd)
		}
		if payload, every := p.Keepalive(); len(payload) > 0 && every > 0 {
			t := time.NewTicker(every)
			defer t.Stop()
			go s.runKeepalive(t.C, payload)
		}
	}

	flush := time.NewTicker(flushInterval)
	defer flush.Stop()

	var (
		batch      []model.Reading
		logBudget  = rawLogRate
		budgetTick = time.NewTicker(time.Second)
		suppressed int
		lastState  time.Time
	)
	defer budgetTick.Stop()

	publish := func() {
		if len(batch) == 0 {
			return
		}
		s.mgr.hub.Broadcast(Event{Type: "readings", At: time.Now(), Readings: batch})
		batch = nil
	}

	frames := s.conn.Frames()
	for {
		select {
		case <-s.done:
			publish()
			return

		case <-budgetTick.C:
			if suppressed > 0 {
				s.log("info", fmt.Sprintf("%d raw frames not shown (log rate limit)", suppressed), nil)
				suppressed = 0
			}
			logBudget = rawLogRate

		case <-flush.C:
			publish()
			// Resend session state (counters, latest values) at a slower beat
			// than the readings. The card only needs to look live; at the
			// flush rate it would be twenty redundant messages a second.
			if time.Since(lastState) >= stateInterval {
				lastState = time.Now()
				view := s.view()
				s.mgr.hub.Broadcast(Event{Type: "session", At: time.Now(), Session: &view})
			}

		case f, ok := <-frames:
			if !ok {
				publish()
				return
			}
			readings := s.dec.Feed(f)

			s.mu.Lock()
			s.frames++
			s.bytes += int64(len(f.Data))
			for i := range readings {
				readings[i].SessionID = s.id
				readings[i].DeviceID = s.device.ID
				if !readings[i].Waveform {
					s.latest[readings[i].Metric] = readings[i]
				}
			}
			s.readings += int64(len(readings))
			s.mu.Unlock()

			batch = append(batch, readings...)
			if len(batch) >= maxBatch {
				publish()
			}

			if logBudget > 0 {
				logBudget--
				s.log("rx", frameLabel(f), f.Data)
			} else {
				suppressed++
			}
		}
	}
}

func (s *session) runKeepalive(tick <-chan time.Time, payload []byte) {
	for {
		select {
		case <-s.done:
			return
		case <-tick:
			if err := s.conn.Write(payload); err != nil {
				return
			}
		}
	}
}

// frameLabel names a frame in the log: a characteristic for BLE, plain rx for
// a byte stream.
func frameLabel(f transport.Frame) string {
	if f.Source == "rx" || f.Source == "" {
		return fmt.Sprintf("%d bytes", len(f.Data))
	}
	if short, ok := shortHex(f.Source); ok {
		return fmt.Sprintf("notify %s (%d bytes)", short, len(f.Data))
	}
	return fmt.Sprintf("notify %s (%d bytes)", f.Source, len(f.Data))
}

func shortHex(uuid string) (string, bool) {
	if len(uuid) == 36 && strings.HasSuffix(strings.ToLower(uuid), "-0000-1000-8000-00805f9b34fb") {
		return "0x" + strings.ToUpper(uuid[4:8]), true
	}
	return "", false
}

func (s *session) shutdown(cause error) {
	s.closeOnce.Do(func() {
		close(s.done)
		_ = s.conn.Close()

		now := time.Now()
		s.mu.Lock()
		s.endedAt = &now
		if cause != nil {
			s.state, s.errMsg = StateError, cause.Error()
		} else {
			s.state = StateClosed
		}
		s.mu.Unlock()

		view := s.view()
		s.mgr.hub.Broadcast(Event{Type: "session", At: now, Session: &view})
		if cause != nil {
			s.log("error", "session ended: "+cause.Error(), nil)
		} else {
			s.log("info", "session closed", nil)
		}
	})
}
