package main

import (
	"context"
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
			fmt.Println("  dex-event-service -list        List all events")
			fmt.Println("  dex-event-service -delete <pattern>  Delete events matching pattern")
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

	// Initialize Redis before starting services
	log.Println("Initializing Redis connection...")
	if err := initializeRedis(); err != nil {
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
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      nil, // Uses DefaultServeMux
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Register handlers
	// /service endpoint is public (for health checks)
	http.HandleFunc("/service", endpoints.ServiceHandler)

	// /events endpoints require authentication
	http.HandleFunc("/events", middleware.ServiceAuthMiddleware(endpoints.EventsHandler(RedisClient)))
	http.HandleFunc("/events/", middleware.ServiceAuthMiddleware(endpoints.EventsHandler(RedisClient)))

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
