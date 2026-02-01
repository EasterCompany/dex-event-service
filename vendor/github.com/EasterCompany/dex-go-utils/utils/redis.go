package utils

import (
	"context"
	"fmt"

	"github.com/EasterCompany/dex-go-utils/config"
	"github.com/redis/go-redis/v9"
)

// GetRedisClient creates and returns a Redis client for the "local-cache-0" service.
func GetRedisClient(ctx context.Context, serviceMap *config.ServiceMapConfig) (*redis.Client, error) {
	// Find local-cache-0 in os services
	var cacheDef *config.ServiceEntry
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

	// Create Redis client options
	host := cacheDef.Domain
	if host == "" {
		host = "127.0.0.1"
	}

	opts := &redis.Options{
		Addr:     fmt.Sprintf("%s:%s", host, cacheDef.Port),
		Password: "",
		DB:       0,
	}

	if cacheDef.Credentials != nil {
		opts.Password = cacheDef.Credentials.Password
		opts.DB = cacheDef.Credentials.DB
	}

	// Create and test client connection
	client := redis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to ping Redis at %s: %w", opts.Addr, err)
	}

	return client, nil
}
