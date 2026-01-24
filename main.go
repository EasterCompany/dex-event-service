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
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/EasterCompany/dex-event-service/config"
	"github.com/EasterCompany/dex-event-service/endpoints"
	"github.com/EasterCompany/dex-event-service/handlers"
	"github.com/EasterCompany/dex-event-service/internal/discord"
	internalHandlers "github.com/EasterCompany/dex-event-service/internal/handlers"
	"github.com/EasterCompany/dex-event-service/internal/model"
	"github.com/EasterCompany/dex-event-service/internal/web"
	"github.com/EasterCompany/dex-event-service/middleware"
	"github.com/EasterCompany/dex-event-service/types"
	"github.com/EasterCompany/dex-event-service/utils"
	"github.com/gorilla/mux"
	redis "github.com/redis/go-redis/v9"
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

	// Declare options here so it's accessible in all scopes
	var options *config.OptionsConfig

	// Create a context for graceful shutdown for redis client
	ctx, redisClientCancel := context.WithCancel(context.Background())
	defer redisClientCancel()

	// Initialize Redis Client for the main service
	redisClient, err := utils.GetRedisClient(ctx)
	if err != nil {
		log.Printf("Warning: Failed to connect to Redis: %v. Event storage and process monitoring will be disabled.", err)
	} else {
		defer func() {
			if err := redisClient.Close(); err != nil {
				log.Printf("Error closing Redis client: %v", err)
			}
		}()
		endpoints.SetRedisClient(redisClient) // Set for endpoints

		// Perform startup cleanup
		performStartupCleanup(ctx, redisClient)
	}

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

	// Load options if not already loaded
	if options == nil {
		options, err = config.LoadOptions()
		if err != nil {
			log.Printf("Warning: Could not load options.json: %v. Master user features may be limited.", err)
		}
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

	// Initialize Cloud Redis Client (Optional)
	cloudURL := os.Getenv("REDIS_CLOUD_URL")
	if cloudURL == "" {
		// Fallback to Upstash RW from service map
		for _, s := range serviceMap.Services["os"] {
			if s.ID == "upstash-redis-rw" {
				// Construct rediss://default:PASSWORD@DOMAIN:6379 (Upstash standard port)
				cloudURL = fmt.Sprintf("rediss://default:%s@%s:6379", s.Credentials.Password, s.Domain)
				break
			}
		}
	}

	if cloudURL != "" {
		opt, err := redis.ParseURL(cloudURL)
		if err != nil {
			log.Printf("Warning: Failed to parse Cloud Redis URL: %v. Cloud mirroring disabled.", err)
		} else {
			cloudClient := redis.NewClient(opt)
			// Test connection
			ctxCloud, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if _, err := cloudClient.Ping(ctxCloud).Result(); err != nil {
				log.Printf("Warning: Failed to connect to Cloud Redis: %v. Cloud mirroring disabled.", err)
				_ = cloudClient.Close()
			} else {
				log.Println("Cloud Redis mirroring enabled.")
				endpoints.StartCloudPulse(ctx, cloudClient, 1*time.Minute)
				defer func() {
					if err := cloudClient.Close(); err != nil {
						log.Printf("Error closing Cloud Redis client: %v", err)
					}
				}()
			}
			cancel()
		}
	}

	// Get port from config, convert to integer.
	port, err := strconv.Atoi(selfConfig.Port)
	if err != nil {
		log.Fatalf("FATAL: Invalid port '%s' for service '%s' in service-map.json: %v", selfConfig.Port, ServiceName, err)
	}

	// Initialize handler registry (config loader)
	log.Println("Loading event handlers...")
	if err := handlers.Initialize(); err != nil {
		log.Fatalf("FATAL: Failed to initialize handlers: %v", err)
	}

	// Resolve Service URLs
	var discordURL, eventURL, ttsURL, webURL, modelHubURL string

	// Helper to find service URL
	getServiceURL := func(id, category string, defaultPort string) string {
		for _, s := range serviceMap.Services[category] {
			if s.ID == id {
				return fmt.Sprintf("http://127.0.0.1:%s", s.Port)
			}
		}
		return fmt.Sprintf("http://127.0.0.1:%s", defaultPort)
	}

	discordURL = getServiceURL("dex-discord-service", "th", "8081")
	eventURL = getServiceURL("dex-event-service", "cs", "8082")
	ttsURL = getServiceURL("dex-tts-service", "be", "8200")
	webURL = getServiceURL("dex-web-service", "be", "8201")
	modelHubURL = getServiceURL("dex-model-service", "co", "8400")

	// Initialize dependencies
	deps := &internalHandlers.Dependencies{
		Redis:           redisClient,
		Model:           model.NewClient(modelHubURL, redisClient),
		Discord:         discord.NewClient(discordURL, eventURL),
		Web:             web.NewClient(webURL),
		Config:          serviceMap,
		Options:         options,
		EventServiceURL: eventURL,
		TTSServiceURL:   ttsURL,
	}

	// Set EventCallback for Model Hub to record model loads/unloads
	deps.Model.SetEventCallback(func(eventType string, data map[string]interface{}) {
		// Create an internal event
		event := types.Event{
			ID:        strconv.FormatInt(time.Now().UnixNano(), 10),
			Service:   ServiceName,
			Timestamp: time.Now().Unix(),
		}
		data["timestamp"] = event.Timestamp
		data["type"] = eventType
		event.Event, _ = json.Marshal(data)

		// Save directly to Redis/Timeline
		_ = handlers.SaveInternalEvent(event)
	})

	// Initialize Executor with dependencies
	handlers.InitExecutor(deps)

	// Create a context for graceful shutdown for HTTP server
	_, httpCancel := context.WithCancel(context.Background())
	defer httpCancel()

	// Setup HTTP server
	router := mux.NewRouter()

	// API Endpoints
	router.HandleFunc("/service", endpoints.ServiceHandler).Methods("GET")
	router.HandleFunc("/events", endpoints.EventsHandler(redisClient)).Methods("POST", "GET", "DELETE")
	router.HandleFunc("/events/{id}", endpoints.EventsHandler(redisClient)).Methods("GET", "PATCH", "DELETE")
	router.HandleFunc("/chores", endpoints.ChoresHandler(redisClient)).Methods("GET", "POST")
	router.HandleFunc("/chores/{path:.*}", endpoints.ChoresHandler(redisClient)).Methods("GET", "PATCH", "DELETE", "POST")
	router.HandleFunc("/system_monitor", endpoints.SystemMonitorHandler).Methods("GET")
	router.HandleFunc("/system/hardware", endpoints.SystemHardwareHandler).Methods("GET")
	router.HandleFunc("/system/options", endpoints.SystemOptionsHandler).Methods("GET", "POST")
	router.HandleFunc("/system/lock", endpoints.SystemLockHandler(redisClient)).Methods("POST")
	router.HandleFunc("/system/service/{action}", endpoints.SystemServiceControlHandler).Methods("POST")
	router.HandleFunc("/processes", endpoints.ListProcessesHandler).Methods("GET")
	router.HandleFunc("/processes", endpoints.HandleProcessRegistration).Methods("POST")
	router.HandleFunc("/processes/{id}", endpoints.HandleProcessUnregistration).Methods("DELETE")
	router.HandleFunc("/logs", endpoints.LogsHandler).Methods("GET", "DELETE")
	router.HandleFunc("/cleanup/alerts", endpoints.CleanupAlertsHandler).Methods("DELETE", "GET")
	router.HandleFunc("/agent/status", endpoints.GetAgentStatusHandler(redisClient)).Methods("GET")
	router.HandleFunc("/agent/pause", endpoints.HandlePauseSystem(redisClient)).Methods("POST")
	router.HandleFunc("/agent/resume", endpoints.HandleResumeSystem(redisClient)).Methods("POST")
	router.HandleFunc("/agent/reset", endpoints.ResetAgentHandler(redisClient)).Methods("POST", "GET")
	router.HandleFunc("/guardian/status", endpoints.GetAgentStatusHandler(redisClient)).Methods("GET") // Keep legacy for fallback during transition
	router.HandleFunc("/guardian/run", endpoints.RunGuardianHandler(redisClient, func(tier int) ([]interface{}, error) {
		if handlers.GuardianTrigger == nil {
			return nil, fmt.Errorf("guardian handler not initialized")
		}
		return handlers.GuardianTrigger(tier)
	})).Methods("POST")
	router.HandleFunc("/analyzer/run", endpoints.RunAnalyzerHandler(redisClient, func() error {
		if handlers.AnalyzerTrigger == nil {
			return fmt.Errorf("analyzer handler not initialized")
		}
		return handlers.AnalyzerTrigger()
	})).Methods("POST")
	router.HandleFunc("/fabricator/run", endpoints.RunFabricatorHandler(redisClient, func() error {
		if handlers.FabricatorTrigger == nil {
			return fmt.Errorf("fabricator handler not initialized")
		}
		return handlers.FabricatorTrigger()
	})).Methods("POST")
	router.HandleFunc("/agent/courier/compressor/run", endpoints.RunCourierCompressorHandler(redisClient, func(force bool, channelID string) error {
		if handlers.CourierTrigger == nil {
			return fmt.Errorf("courier handler not initialized")
		}
		return handlers.CourierTrigger(force, channelID)
	})).Methods("POST")
	router.HandleFunc("/cli/execute", endpoints.HandleCLIExecute).Methods("POST")
	router.HandleFunc("/web/history", endpoints.WebHistoryHandler).Methods("GET", "POST")
	router.HandleFunc("/web/view", endpoints.WebViewHandler).Methods("GET")
	router.HandleFunc("/system/status", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }).Methods("GET", "HEAD")
	router.HandleFunc("/roadmap", endpoints.RoadmapHandler(redisClient)).Methods("GET", "POST", "PATCH", "DELETE")
	router.HandleFunc("/roadmap/stats", endpoints.GetRoadmapStatsHandler()).Methods("GET")
	router.HandleFunc("/roadmap/{id}", endpoints.RoadmapHandler(redisClient)).Methods("GET", "PATCH", "DELETE")

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      middleware.CorsMiddleware(router),
		ReadTimeout:  15 * time.Minute,
		WriteTimeout: 15 * time.Minute,
		IdleTimeout:  60 * time.Minute,
	}

	// Mark service as ready
	utils.SetHealthStatus("OK", "Service is running")

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

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	// Close httpCtx to signal any other background tasks
	httpCancel()

	// Close the executor, which stops all background handlers
	handlers.CloseExecutor()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP shutdown error: %v", err)
	}

	log.Println("Service exited cleanly")
}

// RunTestSuite runs the test suite by triggering the test handler
func RunTestSuite() error {
	log.Println("Loading service configuration...")

	// Load the service map to get Redis configuration
	_, err := config.LoadServiceMap()
	if err != nil {
		return fmt.Errorf("could not load service-map.json: %w", err)
	}

	// Initialize Redis for the test suite
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	testRedisClient, err := utils.GetRedisClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize Redis client for test suite: %w", err)
	}
	defer func() {
		if testRedisClient != nil {
			_ = testRedisClient.Close()
		}
	}()

	// Wait for the event service to be available
	log.Println("Waiting for event service to be available...")
	serviceURL := "http://127.0.0.1:8100/service"
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
	req, _ := http.NewRequest("POST", "http://127.0.0.1:8100/events", bytes.NewBuffer(jsonData))
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

func performStartupCleanup(ctx context.Context, rdb *redis.Client) {
	log.Println("Performing startup cleanup...")

	// 1. Clear Process History
	if err := rdb.Del(ctx, "process:history").Err(); err != nil {
		log.Printf("Failed to clear process history: %v", err)
	}

	// 2. Clear Active Processes and Metrics
	iter := rdb.Scan(ctx, 0, "process:info:*", 0).Iterator()
	for iter.Next(ctx) {
		if err := rdb.Del(ctx, iter.Val()).Err(); err != nil {
			log.Printf("Failed to delete process key %s: %v", iter.Val(), err)
		}
	}
	if err := iter.Err(); err != nil {
		log.Printf("Error scanning process keys: %v", err)
	}

	// Reset State Machine
	rdb.Set(ctx, "system:busy_ref_count", 0, 0)
	rdb.Set(ctx, "system:state", "idle", 0)
	rdb.Set(ctx, "system:last_transition_ts", time.Now().Unix(), 0)

	iterMetrics := rdb.Scan(ctx, 0, "system:metrics:*", 0).Iterator()
	for iterMetrics.Next(ctx) {
		if err := rdb.Del(ctx, iterMetrics.Val()).Err(); err != nil {
			log.Printf("Failed to delete metric key %s: %v", iterMetrics.Val(), err)
		}
	}
	if err := iterMetrics.Err(); err != nil {
		log.Printf("Error scanning metric keys: %v", err)
	}

	// 3. Reset Cognitive Idle Timer (Reset the 5m clock on boot)
	if err := rdb.Set(ctx, "system:last_cognitive_event", time.Now().Unix(), 0).Err(); err != nil {
		log.Printf("Failed to reset cognitive timer: %v", err)
	}

	// 4. Clear Guardian State and Summary Locks
	rdb.Del(ctx, "guardian:active_tier")
	iterLocks := rdb.Scan(ctx, 0, "context:summary:lock:*", 0).Iterator()
	for iterLocks.Next(ctx) {
		rdb.Del(ctx, iterLocks.Val())
	}

	// 5. Clear Logs
	home, err := os.UserHomeDir()
	if err != nil {
		log.Printf("Failed to get user home dir for log cleanup: %v", err)
		return
	}
	logDir := filepath.Join(home, "Dexter", "logs")
	files, err := filepath.Glob(filepath.Join(logDir, "*.log"))
	if err != nil {
		log.Printf("Failed to glob log files: %v", err)
		return
	}

	for _, file := range files {
		if err := os.Truncate(file, 0); err != nil {
			log.Printf("Failed to truncate log file %s: %v", file, err)
		}
	}
	log.Println("Startup cleanup completed.")
}
