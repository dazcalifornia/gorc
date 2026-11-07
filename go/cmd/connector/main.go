// Command connector is the device-connector backend: a Fiber server that
// bridges BLE, classic Bluetooth and USB serial medical devices to a browser
// over REST and a websocket.
package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"

	"github.com/expert/go-connector/internal/api"
	"github.com/expert/go-connector/internal/hub"
	"github.com/expert/go-connector/internal/manager"
	"github.com/expert/go-connector/internal/transport"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	origins := flag.String("cors", "http://localhost:5173,http://127.0.0.1:5173", "comma-separated allowed origins for the Vite dev server")
	static := flag.String("static", "", "optional directory of built frontend assets to serve at /")
	flag.Parse()

	h := hub.New()
	mgr := manager.New(h,
		transport.NewSim(),
		transport.NewSerial(),
		transport.NewBLE(),
		transport.NewBTClassic(),
	)

	app := fiber.New(fiber.Config{
		AppName:               "go-connector",
		DisableStartupMessage: true,
		// The websocket must stay open while a device is idle between
		// measurements, so no write deadline here.
		ReadTimeout: 30 * time.Second,
	})
	app.Use(recover.New())
	app.Use(logger.New(logger.Config{Format: "${time} ${status} ${latency} ${method} ${path}\n"}))
	app.Use(cors.New(cors.Config{AllowOrigins: *origins}))

	api.New(mgr, h).Register(app)

	if *static != "" {
		app.Static("/", *static)
	}

	go func() {
		log.Printf("go-connector listening on %s", *addr)
		for _, t := range mgr.Transports() {
			state := "ready"
			if !t.Available {
				state = "unavailable"
			}
			log.Printf("  transport %-10s %-12s %s", t.Kind, state, t.Note)
		}
		if err := app.Listen(*addr); err != nil {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Println("shutting down; closing device sessions")
	mgr.Shutdown()
	if err := app.ShutdownWithTimeout(5 * time.Second); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
