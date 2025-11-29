package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/EasterCompany/dex-event-service/config"
	"github.com/EasterCompany/dex-event-service/endpoints"
	"github.com/EasterCompany/dex-event-service/handlers"
	"github.com/EasterCompany/dex-event-service/middleware"
	"github.com/EasterCompany/dex-event-service/utils"
)

const ServiceName = "dex-event-service"

var (
	version   string
	branch    string
	commit    string
	buildDate string
	buildYear string
	buildHash string
	arch      string
)

func main() {
	// Handle version/help commands first (before flag parsing)
	if len(os.Args) > 1 {
		arg := os.Args[1]
		switch arg {
		case "version", "--version", "-v":
			// Format version like other services: major.minor.patch.branch.commit.buildDate.arch.buildHash
			utils.SetVersion(version, branch, commit, buildDate, buildYear, buildHash, arch)
			fmt.Println(utils.GetVersion().Str)
			os.Exit(0)
		case "help", "--help", "-h":
			fmt.Println("Dexter Event Service")
			fmt.Println()
			fmt.Println("Usage:")
			fmt.Println("  dex-event-service              Start the event service")
			fmt.Println("  dex-event-service version      Display version information")
			fmt.Println("  dex-event-service test         Run test suite")
			fmt.Println("  dex-event-service -list        List all events")
			fmt.Println("  dex-event-service -delete <pattern>  Delete events matching pattern")
			os.Exit(0)
		case "test":
			// Run test suite
			utils.SetVersion(version, branch, commit, buildDate, buildYear, buildHash, arch)
			if err := RunTestSuite(); err != nil {
				log.Fatalf("Test suite failed: %v", err)
			}
			os.Exit(0)
		}
	}

	// Define CLI flags
	deleteCmd := flag.Bool("delete", false, "Run in delete mode")
	listCmd := flag.Bool("list", false, "List all events")
	flag.Parse()

	// Set the version for the service.
	utils.SetVersion(version, branch, commit, buildDate, buildYear, buildHash, arch)

	// Handle delete mode
	if *deleteCmd {
		patterns := flag.Args()
		if len(patterns) == 0 {
			fmt.Println("Usage: dex-event-service -delete <pattern1> [pattern2] ...")
			fmt.Println("\nExamples:")
			fmt.Println("  dex-event-service -delete '*'                    # Delete all events")
			fmt.Println("  dex-event-service -delete '1*'                   # Delete all starting with 1")
			fmt.Println("  dex-event-service -delete '123-456-789'          # Delete specific event")
			fmt.Println("  dex-event-service -delete '*abc*' '*def*'        # Delete matching patterns")
			fmt.Println("\nFirst run with -list to see all event IDs")
			os.Exit(1)
		}

		if err := DeleteMode(patterns); err != nil {
			log.Fatalf("Delete operation failed: %v", err)
		}
		return
	}

	// Handle list mode
	if *listCmd {
		if err := ListEvents(); err != nil {
			log.Fatalf("List operation failed: %v", err)
		}
		return
	}

	// Load the service map and find our own configuration.
	serviceMap, err := config.LoadServiceMap()
	if err != nil {
		log.Fatalf("FATAL: Could not load service-map.json: %v", err)
	}

	var selfConfig *config.ServiceEntry
	for _, service := range serviceMap.Services["cs"] {
		if service.ID == ServiceName {
			selfConfig = &service
			break
		}
	}

	if selfConfig == nil {
		log.Fatalf("FATAL: Service '%s' not found in service-map.json. Shutting down.", ServiceName)
	}

	// Get port from config, convert to integer.
	port, err := strconv.Atoi(selfConfig.Port)
	if err != nil {
		log.Fatalf("FATAL: Invalid port '%s' for service '%s' in service-map.json: %v", selfConfig.Port, ServiceName, err)
	}

	// Find the Redis service configuration from the service map.
	// For now, we'll hardcode to "local-cache-0" for consistency in local development.
	var redisConfig *config.ServiceEntry
	if osServices, ok := serviceMap.Services["os"]; ok {
		for _, service := range osServices {
			if service.ID == "local-cache-0" { // Using local-cache-0 as default Redis service
				redisConfig = &service
				break
			}
		}
	}

	if redisConfig == nil {
		log.Fatalf("FATAL: Redis service 'local-cache-0' not found in service-map.json. Shutting down.")
	}

	// Initialize Redis before starting services
	log.Println("Initializing Redis connection...")
	if err := initializeRedis(redisConfig); err != nil {
		log.Fatalf("FATAL: Failed to initialize Redis: %v", err)
	}
	log.Println("Redis connected successfully")

	// Initialize handler registry
	log.Println("Loading event handlers...")
	if err := handlers.Initialize(); err != nil {
		log.Fatalf("FATAL: Failed to initialize handlers: %v", err)
	}

	// Create a context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the core event logic in a goroutine
	go func() {
		log.Println("Core Event Logic: Starting...")
		if err := RunCoreLogic(ctx); err != nil {
			log.Printf("Core Logic Error: %v", err)
			// Trigger shutdown if core logic fails
			cancel()
		}
		log.Println("Core Event Logic: Stopped")
	}()

	// Configure HTTP server
	mux := http.NewServeMux()

	// Register handlers
	// /service endpoint is public (for health checks)
	mux.HandleFunc("/service", endpoints.ServiceHandler)

	// /events endpoints require authentication
	mux.HandleFunc("/events", middleware.ServiceAuthMiddleware(endpoints.EventsHandler(RedisClient)))
	mux.HandleFunc("/events/", middleware.ServiceAuthMiddleware(endpoints.EventsHandler(RedisClient)))
	mux.HandleFunc("/system_monitor_metrics", endpoints.SystemMonitorHandler)

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      middleware.CorsMiddleware(mux),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start HTTP server in a goroutine
	go func() {
		fmt.Printf("Starting %s on :%d\n", ServiceName, port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server crashed: %v", err)
		}
	}()

	// Wait for shutdown signal (SIGTERM from systemd or SIGINT from Ctrl+C)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	// Block here until signal received
	<-stop
	log.Println("Shutting down service...")

	// Graceful cleanup
	utils.SetHealthStatus("SHUTTING_DOWN", "Service is shutting down")
	cancel() // Signals the core logic to stop

	// Give the HTTP server 5 seconds to finish current requests
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP shutdown error: %v", err)
	}

	log.Println("Service exited cleanly")
}

// RunTestSuite runs the test suite by triggering the test handler
func RunTestSuite() error {
	log.Println("Loading service configuration...")

	// Load the service map to get Redis configuration
	serviceMap, err := config.LoadServiceMap()
	if err != nil {
		return fmt.Errorf("could not load service-map.json: %w", err)
	}

	// Find Redis configuration
	var redisConfig *config.ServiceEntry
	if osServices, ok := serviceMap.Services["os"]; ok {
		for _, service := range osServices {
			if service.ID == "local-cache-0" {
				redisConfig = &service
				break
			}
		}
	}

	if redisConfig == nil {
		return fmt.Errorf("redis service 'local-cache-0' not found in service-map.json")
	}

	// Initialize Redis
	log.Println("Connecting to Redis...")
	if err := initializeRedis(redisConfig); err != nil {
		return fmt.Errorf("failed to initialize Redis: %w", err)
	}
	defer func() {
		if RedisClient != nil {
			_ = RedisClient.Close()
		}
	}()

	// Wait for the event service to be available
	log.Println("Waiting for event service to be available...")
	serviceURL := "http://localhost:8100/service"
	client := &http.Client{Timeout: 2 * time.Second}

	maxRetries := 10
	for i := 0; i < maxRetries; i++ {
		resp, err := client.Get(serviceURL)
		if err == nil && resp.StatusCode == 200 {
			_ = resp.Body.Close()
			log.Println("Event service is available")
			break
		}
		if resp != nil {
			_ = resp.Body.Close()
		}

		if i == maxRetries-1 {
			return fmt.Errorf("event service not available after %d attempts - make sure 'dex-event-service' is running", maxRetries)
		}

		time.Sleep(1 * time.Second)
	}

	// Trigger the test by creating an event with the test handler
	log.Println("Triggering test suite...")

	payload := map[string]interface{}{
		"service": "dex-event-service",
		"event": map[string]interface{}{
			"type":    "log_entry",
			"level":   "info",
			"message": "Manual test suite triggered via CLI",
		},
		"handler":      "test",
		"handler_mode": "sync", // Wait for test to complete
	}

	jsonData, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", "http://localhost:8100/events", bytes.NewBuffer(jsonData))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Name", "dex-event-service")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to trigger test: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("test suite returned HTTP %d", resp.StatusCode)
	}

	log.Println("Test suite completed successfully")
	return nil
}
