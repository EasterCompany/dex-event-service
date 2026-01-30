package endpoints

import (
	"fmt"
	"net/http"

	"github.com/redis/go-redis/v9"
)

// RunCourierCompressorHandler triggers the courier compressor protocols manually.
func RunCourierCompressorHandler(RDB *redis.Client, trigger func(bool, string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		force := r.URL.Query().Get("force") == "true"
		channelID := r.URL.Query().Get("channel_id")

		if err := trigger(force, channelID); err != nil {
			http.Error(w, fmt.Sprintf("Courier Compressor run failed: %v", err), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status": "triggered"}`))
	}
}
