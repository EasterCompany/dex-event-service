package endpoints

import (
	"net/http"

	"github.com/redis/go-redis/v9"
)

// RunAnalyzerHandler triggers the analyzer protocols manually.
func RunAnalyzerHandler(RDB *redis.Client, trigger func() error) http.HandlerFunc {
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
