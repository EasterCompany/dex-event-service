package handlers

import (
	"context"
)

// BackgroundHandler is the interface for handlers that run in their own goroutine
// and manage their own lifecycle (e.g., proactive tasks, polling).
type BackgroundHandler interface {
	Init(ctx context.Context) error
	Close() error
}
