// Package api exposes the connector over HTTP: a small REST surface for
// discovery and session control, and one websocket that streams every reading,
// state change and raw frame to the browser.
package api

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/contrib/websocket"
	"github.com/gofiber/fiber/v2"

	"github.com/expert/go-connector/internal/decode"
	"github.com/expert/go-connector/internal/hub"
	"github.com/expert/go-connector/internal/manager"
	"github.com/expert/go-connector/internal/transport"
)

// scanTimeout bounds a discovery request. BLE needs seconds of listening;
// serial enumeration is instant.
const scanTimeout = 10 * time.Second

type Server struct {
	mgr *manager.Manager
	hub *hub.Hub
}

func New(m *manager.Manager, h *hub.Hub) *Server { return &Server{mgr: m, hub: h} }

// Register mounts the routes on a Fiber app.
func (s *Server) Register(app *fiber.App) {
	api := app.Group("/api")
	api.Get("/health", s.health)
	api.Get("/transports", s.transports)
	api.Get("/profiles", s.profiles)
	api.Get("/scan", s.scan)
	api.Get("/sessions", s.listSessions)
	api.Post("/sessions", s.openSession)
	api.Delete("/sessions", s.forgetSessions)
	api.Get("/sessions/:id", s.getSession)
	api.Delete("/sessions/:id", s.closeSession)
	api.Post("/sessions/:id/write", s.writeSession)

	// The upgrade guard must run before the handler so a plain GET gets a 426
	// rather than a confusing protocol error.
	app.Use("/ws", func(c *fiber.Ctx) error {
		if websocket.IsWebSocketUpgrade(c) {
			return c.Next()
		}
		return fiber.ErrUpgradeRequired
	})
	app.Get("/ws", websocket.New(s.stream))
}

func (s *Server) health(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{
		"status":    "ok",
		"sessions":  len(s.mgr.Sessions()),
		"wsClients": s.hub.Count(),
		"time":      time.Now(),
	})
}

func (s *Server) transports(c *fiber.Ctx) error {
	return c.JSON(s.mgr.Transports())
}

func (s *Server) profiles(c *fiber.Ctx) error {
	return c.JSON(decode.Profiles())
}

// scan runs discovery on one transport: GET /api/scan?transport=ble
func (s *Server) scan(c *fiber.Ctx) error {
	kind := transport.Kind(c.Query("transport"))
	if kind == "" {
		return fail(c, fiber.StatusBadRequest, errors.New("transport query parameter is required"))
	}

	ctx, cancel := context.WithTimeout(c.UserContext(), scanTimeout)
	defer cancel()

	devices, err := s.mgr.Scan(ctx, kind)
	if err != nil {
		return fail(c, fiber.StatusBadGateway, err)
	}
	return c.JSON(fiber.Map{"transport": kind, "devices": devices})
}

func (s *Server) listSessions(c *fiber.Ctx) error {
	return c.JSON(s.mgr.Sessions())
}

func (s *Server) openSession(c *fiber.Ctx) error {
	var req manager.OpenRequest
	if err := c.BodyParser(&req); err != nil {
		return fail(c, fiber.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
	}

	// Opening a link can block on a radio or a port, so give it its own
	// timeout rather than inheriting the request's.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sess, err := s.mgr.Open(ctx, req)
	if err != nil {
		return fail(c, fiber.StatusBadGateway, err)
	}
	return c.Status(fiber.StatusCreated).JSON(sess)
}

func (s *Server) getSession(c *fiber.Ctx) error {
	sess, ok := s.mgr.Get(c.Params("id"))
	if !ok {
		return fail(c, fiber.StatusNotFound, fmt.Errorf("no session %q", c.Params("id")))
	}
	return c.JSON(sess)
}

func (s *Server) closeSession(c *fiber.Ctx) error {
	if err := s.mgr.Close(c.Params("id")); err != nil {
		return fail(c, fiber.StatusNotFound, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func (s *Server) forgetSessions(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"removed": s.mgr.Forget()})
}

// writeRequest sends bytes to a device as either hex or literal text, which
// covers both binary wake-up sequences and the ASCII commands scales use.
type writeRequest struct {
	Hex  string `json:"hex,omitempty"`
	Text string `json:"text,omitempty"`
}

func (s *Server) writeSession(c *fiber.Ctx) error {
	var req writeRequest
	if err := c.BodyParser(&req); err != nil {
		return fail(c, fiber.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
	}

	var payload []byte
	switch {
	case req.Hex != "":
		clean := strings.NewReplacer(" ", "", "-", "", ":", "", "0x", "").Replace(req.Hex)
		b, err := hex.DecodeString(clean)
		if err != nil {
			return fail(c, fiber.StatusBadRequest, fmt.Errorf("hex payload: %w", err))
		}
		payload = b
	case req.Text != "":
		// \r is the usual terminator for serial scale commands, so accept it
		// written out rather than requiring the caller to send raw bytes.
		payload = []byte(strings.NewReplacer(`\r`, "\r", `\n`, "\n").Replace(req.Text))
	default:
		return fail(c, fiber.StatusBadRequest, errors.New("provide either hex or text"))
	}

	if err := s.mgr.Write(c.Params("id"), payload); err != nil {
		return fail(c, fiber.StatusBadGateway, err)
	}
	return c.JSON(fiber.Map{"written": len(payload)})
}

// stream pushes hub events to one websocket client.
//
// Only the hub writes to the socket; the read loop exists purely to notice the
// client going away, since a websocket read error is the reliable signal for
// that.
func (s *Server) stream(c *websocket.Conn) {
	client := s.hub.Subscribe()
	defer s.hub.Unsubscribe(client)

	// Send current state first so the UI renders without waiting for traffic.
	if err := c.WriteJSON(s.mgr.Snapshot()); err != nil {
		return
	}

	gone := make(chan struct{})
	go func() {
		defer close(gone)
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-gone:
			return
		case <-client.Closed():
			// Dropped for falling behind; tell the client why before hanging up.
			_ = c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(
				websocket.ClosePolicyViolation, "client too slow"))
			return
		case payload := <-client.Send():
			if err := c.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}
		}
	}
}

func fail(c *fiber.Ctx, status int, err error) error {
	return c.Status(status).JSON(fiber.Map{"error": err.Error()})
}
