package endpoints

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
)

// AnalystStatusResponse returns the last and next run times for analyst tiers.
type AnalystStatusResponse struct {
	Guardian struct {
		LastRun int64 `json:"last_run"`
		NextRun int64 `json:"next_run"`
	} `json:"guardian"`
	Architect struct {
		LastRun int64 `json:"last_run"`
		NextRun int64 `json:"next_run"`
	} `json:"architect"`
	Strategist struct {
		LastRun int64 `json:"last_run"`
		NextRun int64 `json:"next_run"`
	} `json:"strategist"`
	SystemIdleTime int64 `json:"system_idle_time"`
}

// GetAnalystStatusHandler returns the current timing status of the analyst worker.
func GetAnalystStatusHandler(redisClient *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if redisClient == nil {
			http.Error(w, "Redis not connected", http.StatusServiceUnavailable)
			return
		}

		ctx := context.Background()
		var status AnalystStatusResponse

		// Last Analysis Key (Guardian/Global)
		lastAnalysisTS, _ := redisClient.Get(ctx, "analyst:last_analysis_ts").Int64()
		status.Guardian.LastRun = lastAnalysisTS
		status.Guardian.NextRun = lastAnalysisTS + 300 // 5 minutes

		// Architect
		lastArchTS, _ := redisClient.Get(ctx, "analyst:last_run:architect").Int64()
		status.Architect.LastRun = lastArchTS
		status.Architect.NextRun = lastArchTS + 900 // 15 minutes

		// Strategist
		lastStratTS, _ := redisClient.Get(ctx, "analyst:last_run:strategist").Int64()
		status.Strategist.LastRun = lastStratTS
		status.Strategist.NextRun = lastStratTS + 3600 // 1 hour

		// System Idle Time
		lastEventTS, _ := redisClient.Get(ctx, "system:last_event_ts").Int64()
		if lastEventTS > 0 {
			now := time.Now().Unix()
			status.SystemIdleTime = now - lastEventTS
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(status); err != nil {
			log.Printf("Error encoding analyst status: %v", err)
		}
	}
}

// ResetAnalystHandler resets the timers for analyst tiers.
func ResetAnalystHandler(redisClient *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if redisClient == nil {
			http.Error(w, "Redis not connected", http.StatusServiceUnavailable)
			return
		}

		ctx := context.Background()
		tier := r.URL.Query().Get("tier")

		if tier == "" || tier == "all" || tier == "strategist" {
			redisClient.Set(ctx, "analyst:last_run:strategist", 0, 0)
		}
		if tier == "all" || tier == "architect" {
			redisClient.Set(ctx, "analyst:last_run:architect", 0, 0)
		}
		if tier == "all" || tier == "guardian" {
			redisClient.Set(ctx, "analyst:last_analysis_ts", 0, 0)
		}

		// Also reset the check timer in the worker by marking it as needing immediate check
		// We can't directly access the worker's internal state, but resetting the last check keys
		// will cause it to trigger on its next tick.

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Analyst timers reset successfully"))
	}
}
