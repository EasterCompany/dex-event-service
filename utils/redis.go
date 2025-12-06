package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/EasterCompany/dex-event-service/config" // Import the config package
	"github.com/redis/go-redis/v9"
)

// GetRedisClient creates and returns a Redis client for local-cache-0
func GetRedisClient(ctx context.Context) (*redis.Client, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	serviceMapPath := filepath.Join(home, "Dexter", "config", "service-map.json")
	data, err := os.ReadFile(serviceMapPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read service-map.json: %w", err)
	}

	var serviceMap config.ServiceMapConfig // Use config.ServiceMapConfig
	if err := json.Unmarshal(data, &serviceMap); err != nil {
		return nil, fmt.Errorf("failed to parse service-map.json: %w", err)
	}

	var cacheDef *config.ServiceEntry // Use config.ServiceEntry
	if osServices, ok := serviceMap.Services["os"]; ok {
		for i := range osServices {
			if osServices[i].ID == "local-cache-0" {
				cacheDef = &osServices[i]
				break
			}
		}
	}

	if cacheDef == nil {
		return nil, fmt.Errorf("local-cache-0 service not found in service-map.json")
	}

	opts := &redis.Options{
		Addr:     fmt.Sprintf("%s:%s", cacheDef.Domain, cacheDef.Port),
		Password: "",
		DB:       0,
	}

	if cacheDef.Credentials != nil { // Check for credentials existence
		opts.Password = cacheDef.Credentials.Password
		opts.DB = cacheDef.Credentials.DB
	}

	client := redis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to ping Redis at %s: %w", opts.Addr, err)
	}

	return client, nil
}
