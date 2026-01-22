package endpoints

import (
	"fmt"
	"net/http"

	"github.com/redis/go-redis/v9"
)

// RunCourierCompressorHandler triggers the courier compressor protocols manually.
func RunCourierCompressorHandler(redisClient *redis.Client, trigger func() error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := trigger(); err != nil {
			http.Error(w, fmt.Sprintf("Courier Compressor run failed: %v", err), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status": "triggered"}`))
	}
}
