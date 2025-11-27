package main

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
)

// DeleteMode runs the event deletion CLI tool
func DeleteMode(patterns []string) error {
	ctx := context.Background()

	// Initialize Redis
	log.Println("Connecting to Redis for deletion operation...")
	if err := initializeRedis(); err != nil {
		return fmt.Errorf("failed to connect to Redis: %w", err)
	}
	defer func() {
		if RedisClient != nil {
			if err := RedisClient.Close(); err != nil {
				log.Printf("Failed to close Redis connection: %v", err)
			}
		}
	}()

	log.Printf("Connected to Redis at %s", RedisClient.Options().Addr)
	log.Printf("Deletion patterns: %v", patterns)

	// Get all event IDs from the timeline
	allEventIDs, err := RedisClient.ZRange(ctx, "events:timeline", 0, -1).Result()
	if err != nil {
		return fmt.Errorf("failed to fetch event IDs: %w", err)
	}

	log.Printf("Found %d total events in timeline", len(allEventIDs))

	if len(allEventIDs) == 0 {
		log.Println("No events found in database")
		return nil
	}

	// Convert patterns to regex and find matching event IDs
	matchingIDs := []string{}
	for _, eventID := range allEventIDs {
		if matchesAnyPattern(eventID, patterns) {
			matchingIDs = append(matchingIDs, eventID)
		}
	}

	if len(matchingIDs) == 0 {
		log.Printf("No events matched the patterns: %v", patterns)
		return nil
	}

	log.Printf("Found %d events matching deletion patterns", len(matchingIDs))

	// Confirm deletion
	fmt.Printf("\n⚠️  WARNING: About to delete %d event(s):\n", len(matchingIDs))
	if len(matchingIDs) <= 10 {
		for _, id := range matchingIDs {
			fmt.Printf("  - %s\n", id)
		}
	} else {
		for i := 0; i < 5; i++ {
			fmt.Printf("  - %s\n", matchingIDs[i])
		}
		fmt.Printf("  ... and %d more\n", len(matchingIDs)-5)
	}
	fmt.Printf("\nThis action CANNOT be undone.\n")
	fmt.Printf("Type 'yes' to confirm deletion: ")

	var confirmation string
	_, _ = fmt.Scanln(&confirmation) // Ignore input errors

	if confirmation != "yes" {
		log.Println("Deletion cancelled by user")
		return nil
	}

	// Delete events
	deletedCount := 0
	for _, eventID := range matchingIDs {
		if err := deleteEvent(ctx, eventID); err != nil {
			log.Printf("Error deleting event %s: %v", eventID, err)
			continue
		}
		deletedCount++
	}

	log.Printf("✓ Successfully deleted %d out of %d events", deletedCount, len(matchingIDs))
	return nil
}

// matchesAnyPattern checks if an event ID matches any of the given patterns
func matchesAnyPattern(eventID string, patterns []string) bool {
	for _, pattern := range patterns {
		if matchesPattern(eventID, pattern) {
			return true
		}
	}
	return false
}

// matchesPattern converts glob-style pattern to regex and matches
func matchesPattern(eventID, pattern string) bool {
	// Convert glob pattern to regex
	// * becomes .*
	// ? becomes .
	// Escape other regex special characters
	regexPattern := regexp.QuoteMeta(pattern)
	regexPattern = strings.ReplaceAll(regexPattern, `\*`, ".*")
	regexPattern = strings.ReplaceAll(regexPattern, `\?`, ".")
	regexPattern = "^" + regexPattern + "$"

	matched, err := regexp.MatchString(regexPattern, eventID)
	if err != nil {
		log.Printf("Invalid pattern '%s': %v", pattern, err)
		return false
	}

	return matched
}

// deleteEvent removes an event from all Redis structures
func deleteEvent(ctx context.Context, eventID string) error {
	// First, get the event to find out which service it belongs to
	eventKey := "event:" + eventID
	eventData, err := RedisClient.Get(ctx, eventKey).Result()

	var serviceName string
	if err == nil {
		// Parse the event to get the service name
		// Simple JSON parsing to extract service field
		if idx := strings.Index(eventData, `"service":"`); idx != -1 {
			start := idx + len(`"service":"`)
			if end := strings.Index(eventData[start:], `"`); end != -1 {
				serviceName = eventData[start : start+end]
			}
		}
	}

	// Use a pipeline for atomic deletion
	pipe := RedisClient.Pipeline()

	// Delete the event data
	pipe.Del(ctx, eventKey)

	// Remove from global timeline
	pipe.ZRem(ctx, "events:timeline", eventID)

	// Remove from service-specific timeline if we know the service
	if serviceName != "" {
		serviceTimelineKey := fmt.Sprintf("events:service:%s", serviceName)
		pipe.ZRem(ctx, serviceTimelineKey, eventID)
	}

	// Execute the pipeline
	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to delete event %s: %w", eventID, err)
	}

	return nil
}

// ListEvents shows all events for deletion selection
func ListEvents() error {
	ctx := context.Background()

	// Initialize Redis
	log.Println("Connecting to Redis...")
	if err := initializeRedis(); err != nil {
		return fmt.Errorf("failed to connect to Redis: %w", err)
	}
	defer func() {
		if RedisClient != nil {
			if err := RedisClient.Close(); err != nil {
				log.Printf("Failed to close Redis connection: %v", err)
			}
		}
	}()

	// Get all event IDs
	allEventIDs, err := RedisClient.ZRange(ctx, "events:timeline", 0, -1).Result()
	if err != nil {
		return fmt.Errorf("failed to fetch event IDs: %w", err)
	}

	if len(allEventIDs) == 0 {
		fmt.Println("No events found in database")
		return nil
	}

	fmt.Printf("Total events: %d\n\n", len(allEventIDs))
	fmt.Println("Event IDs:")
	for i, eventID := range allEventIDs {
		// Fetch event details
		eventKey := "event:" + eventID
		eventData, err := RedisClient.Get(ctx, eventKey).Result()

		var serviceName string
		if err == nil {
			if idx := strings.Index(eventData, `"service":"`); idx != -1 {
				start := idx + len(`"service":"`)
				if end := strings.Index(eventData[start:], `"`); end != -1 {
					serviceName = eventData[start : start+end]
				}
			}
		}

		fmt.Printf("%4d. %s  [%s]\n", i+1, eventID, serviceName)
	}

	return nil
}
