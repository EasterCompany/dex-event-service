package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/EasterCompany/dex-event-service/config"
	"github.com/EasterCompany/dex-event-service/utils"
	"github.com/redis/go-redis/v9"
)

// RedisClient is a global Redis client accessible by all endpoints
var RedisClient *redis.Client

// initializeRedis sets up the Redis connection using the provided configuration.
func initializeRedis(redisConfig *config.ServiceEntry) error {
	redisAddr := fmt.Sprintf("%s:%s", redisConfig.Domain, redisConfig.Port)

	// Parse credentials
	var redisPassword string
	redisDB := 0

	if creds, ok := redisConfig.Credentials.(map[string]interface{}); ok {
		if password, found := creds["password"].(string); found {
			redisPassword = password
		}
		if db, found := creds["db"].(float64); found { // JSON numbers are float64
			redisDB = int(db)
		}
	} else {
		log.Printf("Warning: Redis credentials not found or invalid for service %s", redisConfig.ID)
	}

	RedisClient = redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: redisPassword,
		DB:       redisDB,
	})

	// Test Redis connection with a ping
	ctx := context.Background()
	if err := RedisClient.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("failed to connect to Redis at %s: %w", redisAddr, err)
	}

	log.Printf("Connected to Redis at %s", redisAddr)
	return nil
}

// RunCoreLogic represents the persistent core functionality of the service.
// This runs continuously in a goroutine, processing events and maintaining
// connections to databases, message queues, or other persistent resources.
func RunCoreLogic(ctx context.Context) error {
	// Initialize resources (database connections, message queues, etc.)
	if err := initializePersistentResources(ctx); err != nil {
		utils.SetHealthStatus("ERROR", "Failed to initialize resources: "+err.Error())
		return err
	}
	defer cleanupPersistentResources()

	// Mark service as healthy once initialization is complete
	utils.SetHealthStatus("OK", "Service is running normally")
	log.Println("Core Logic: Initialization complete, service is healthy")

	// Create a ticker for periodic tasks (adjust interval as needed)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Main event loop
	for {
		select {
		case <-ctx.Done():
			// Context cancelled - shutdown requested
			log.Println("Core Logic: Shutdown signal received, cleaning up...")
			utils.SetHealthStatus("SHUTTING_DOWN", "Core logic is shutting down")
			return nil

		case <-ticker.C:
			// Periodic task execution
			if err := processPersistentTasks(ctx); err != nil {
				log.Printf("Core Logic: Error processing tasks: %v", err)
				utils.SetHealthStatus("DEGRADED", "Error processing tasks: "+err.Error())
				// Continue running even if a task fails
				// You can decide to return here if the error is critical
			} else {
				// Ensure health status is OK after successful processing
				utils.SetHealthStatus("OK", "Service is running normally")
			}
		}
	}
}

// initializePersistentResources sets up database connections, message queue
// connections, or any other resources that need to persist for the lifetime
// of the service. Note: Redis is initialized separately in main().
func initializePersistentResources(ctx context.Context) error {
	log.Println("Core Logic: Initializing persistent resources...")

	// Redis is already initialized in main()

	// TODO: Initialize other persistent resources
	// - Database connections
	// - Message queue connections
	// - WebSocket connections
	// - File watchers
	// - etc.

	log.Println("Core Logic: Persistent resources initialized successfully")

	// Auto-validate on startup if needed
	go autoValidateOnStartup()

	return nil
}

// autoValidateOnStartup triggers the test handler if:
// 1. There are no events in Redis (first run ever), OR
// 2. The binary is newer than the last event (new deployment)
func autoValidateOnStartup() {
	// Give the service a moment to fully start up
	time.Sleep(2 * time.Second)

	ctx := context.Background()

	// Check if there are any events in Redis
	eventCount, err := RedisClient.ZCard(ctx, "events:timeline").Result()
	if err != nil {
		log.Printf("Auto-validate: Could not check event count: %v", err)
		return
	}

	shouldTest := false
	reason := ""

	if eventCount == 0 {
		// No events ever recorded - definitely test
		shouldTest = true
		reason = "first run (no events in Redis)"
	} else {
		// Check if binary is newer than last event
		binaryPath := os.Args[0]
		binaryInfo, err := os.Stat(binaryPath)
		if err == nil {
			// Get most recent event timestamp
			events, err := RedisClient.ZRevRangeWithScores(ctx, "events:timeline", 0, 0).Result()
			if err == nil && len(events) > 0 {
				lastEventTime := int64(events[0].Score)
				binaryTime := binaryInfo.ModTime().Unix()

				if binaryTime > lastEventTime {
					shouldTest = true
					reason = "new build detected (binary newer than last event)"
				}
			}
		}
	}

	if !shouldTest {
		log.Println("Auto-validate: Skipping (service already validated)")
		return
	}

	log.Printf("Auto-validate: Triggering self-test (%s)", reason)

	// Create test event with handler
	payload := map[string]interface{}{
		"service": "dex-event-service",
		"event": map[string]interface{}{
			"type":    "log_entry",
			"level":   "info",
			"message": fmt.Sprintf("Auto-validation triggered: %s", reason),
		},
		"handler":      "test",
		"handler_mode": "async", // Don't block startup
	}

	jsonData, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", "http://localhost:8100/events", bytes.NewBuffer(jsonData))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Name", "dex-event-service")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Auto-validate: Failed to trigger test: %v", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		log.Println("Auto-validate: Self-test triggered successfully (running in background)")
	} else {
		log.Printf("Auto-validate: Failed to trigger test (HTTP %d)", resp.StatusCode)
	}
}

// cleanupPersistentResources closes all persistent connections and releases
// resources when the service shuts down.
func cleanupPersistentResources() {
	log.Println("Core Logic: Cleaning up persistent resources...")

	// Close Redis connection
	if RedisClient != nil {
		if err := RedisClient.Close(); err != nil {
			log.Printf("Error closing Redis connection: %v", err)
		} else {
			log.Println("Redis connection closed")
		}
	}

	// TODO: Clean up any other resources
	// - Close database connections
	// - Close message queue connections
	// - Close WebSocket connections
	// - Stop file watchers
	// - etc.

	log.Println("Core Logic: Cleanup complete")
}

// processPersistentTasks performs the actual work of the service.
// This is called periodically by the ticker in RunCoreLogic.
func processPersistentTasks(ctx context.Context) error {
	// TODO: Implement your core business logic here
	// Examples:
	// - Check for new events in a database
	// - Poll a message queue for new messages
	// - Process scheduled jobs
	// - Monitor external systems
	// - Aggregate data
	// - Send notifications
	// - etc.

	// Example placeholder logic:
	// log.Println("Core Logic: Processing events...")

	// Check if context is cancelled before doing expensive work
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		// Continue processing
	}

	// Your actual processing logic goes here

	return nil
}
