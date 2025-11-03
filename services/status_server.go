package services

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"sync/atomic"
	"time"
)

// StatusServer provides HTTP status endpoint for this service
type StatusServer struct {
	startTime time.Time
	port      int
	version   string

	// Metrics
	eventsReceived  atomic.Uint64
	eventsProcessed atomic.Uint64
	eventsFailed    atomic.Uint64
}

// NewStatusServer creates a new status server instance
func NewStatusServer(port int, version string) *StatusServer {
	return &StatusServer{
		startTime: time.Now(),
		port:      port,
		version:   version,
	}
}

// Start begins the HTTP status server
func (ss *StatusServer) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", ss.handleStatus)
	mux.HandleFunc("/health", ss.handleHealth)

	addr := fmt.Sprintf(":%d", ss.port)
	log.Printf("[STATUS] Starting status server on 0.0.0.0:%d", ss.port)

	go func() {
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Printf("[STATUS] Server error: %v", err)
		}
	}()

	return nil
}

// handleStatus returns detailed service status
func (ss *StatusServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	uptime := time.Since(ss.startTime)

	// Get memory stats
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	status := map[string]interface{}{
		"service":   "dex-event-service",
		"status":    "OK",
		"version":   ss.version,
		"uptime":    int(uptime.Seconds()),
		"timestamp": time.Now().Unix(),
		"metrics": map[string]interface{}{
			"events_received":  ss.eventsReceived.Load(),
			"events_processed": ss.eventsProcessed.Load(),
			"events_failed":    ss.eventsFailed.Load(),
			"goroutines":       runtime.NumGoroutine(),
			"memory_alloc_mb":  float64(m.Alloc) / 1024 / 1024,
			"memory_total_mb":  float64(m.TotalAlloc) / 1024 / 1024,
			"memory_sys_mb":    float64(m.Sys) / 1024 / 1024,
			"gc_runs":          m.NumGC,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(status); err != nil {
		log.Printf("[STATUS] Error encoding status: %v", err)
	}
}

// handleHealth returns simple health check
func (ss *StatusServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{
		"status": "OK",
	}); err != nil {
		log.Printf("[STATUS] Error encoding health check: %v", err)
	}
}

// IncrementEventsReceived increments the events received counter
func (ss *StatusServer) IncrementEventsReceived() {
	ss.eventsReceived.Add(1)
}

// IncrementEventsProcessed increments the events processed counter
func (ss *StatusServer) IncrementEventsProcessed() {
	ss.eventsProcessed.Add(1)
}

// IncrementEventsFailed increments the events failed counter
func (ss *StatusServer) IncrementEventsFailed() {
	ss.eventsFailed.Add(1)
}
