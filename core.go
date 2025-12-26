package main

import (
	"context"
	"fmt"
	"log"
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

	if redisConfig.Credentials != nil {
		redisPassword = redisConfig.Credentials.Password
		redisDB = redisConfig.Credentials.DB
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

	// Initialize cognitive idle timer if not present
	if RedisClient != nil {
		exists, err := RedisClient.Exists(ctx, "system:last_cognitive_event").Result()
		if err == nil && exists == 0 {
			RedisClient.Set(ctx, "system:last_cognitive_event", time.Now().Unix(), 0)
			log.Println("Core Logic: Initialized system:last_cognitive_event")
		}
	}

	// Redis is already initialized in main()

	// TODO: Initialize other persistent resources
	// - Database connections
	// - Message queue connections
	// - WebSocket connections
	// - File watchers
	// - etc.

	log.Println("Core Logic: Persistent resources initialized successfully")

	return nil
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
