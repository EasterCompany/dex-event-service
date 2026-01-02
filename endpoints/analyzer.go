package endpoints

import (
	"context"
	"net/http"

	"github.com/EasterCompany/dex-event-service/utils"
	"github.com/redis/go-redis/v9"
)

// ResetAnalyzerHandler resets the timers for analyzer protocols.
func ResetAnalyzerHandler(redisClient *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if redisClient == nil {
			http.Error(w, "Redis not connected", http.StatusServiceUnavailable)
			return
		}

		ctx := context.Background()
		query := r.URL.Query()
		tier := query.Get("tier")
		if tier == "" {
			tier = query.Get("protocol")
		}

		if tier == "" || tier == "all" || tier == "synthesis" {
			redisClient.Set(ctx, "analyzer:last_run:synthesis", 0, utils.DefaultTTL)
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Analyzer protocols reset successfully"))
	}
}

// RunAnalyzerHandler triggers the analyzer protocols manually.
func RunAnalyzerHandler(redisClient *redis.Client, trigger func() error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if trigger == nil {
			http.Error(w, "Analyzer trigger not initialized", http.StatusServiceUnavailable)
			return
		}

		// Run in background so we don't timeout the HTTP request
		go func() {
			_ = trigger()
		}()

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Analyzer synthesis protocol triggered in background"))
	}
}
