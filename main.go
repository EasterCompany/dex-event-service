package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/EasterCompany/dex-event-service/services"
)

const (
	ServicePort    = 8100
	ServiceVersion = "1.0.0"
)

var (
	statusServer *services.StatusServer
)

func main() {
	log.Printf("[MAIN] Starting dex-event-service v%s", ServiceVersion)

	// Initialize status server
	statusServer = services.NewStatusServer(ServicePort, ServiceVersion)
	if err := statusServer.Start(); err != nil {
		log.Fatalf("[MAIN] Failed to start status server: %v", err)
	}

	log.Printf("[MAIN] dex-event-service is running on http://127.0.0.1:%d", ServicePort)
	log.Printf("[MAIN] Status endpoint: http://127.0.0.1:%d/status", ServicePort)
	log.Printf("[MAIN] Health endpoint: http://127.0.0.1:%d/health", ServicePort)

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	log.Printf("[MAIN] Shutting down dex-event-service...")
	log.Printf("[MAIN] Shutdown complete")
}
