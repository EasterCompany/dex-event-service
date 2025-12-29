package endpoints

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/EasterCompany/dex-event-service/utils"
	"github.com/redis/go-redis/v9"
)

// GuardianStatusResponse returns the last and next run times for guardian tiers.
type GuardianStatusResponse struct {
	ActiveTier string `json:"active_tier,omitempty"`
	Tier1      struct {
		LastRun          int64  `json:"last_run"`
		NextRun          int64  `json:"next_run"`
		Attempts         int64  `json:"attempts"`
		Failures         int64  `json:"failures"`
		AbsoluteFailures int64  `json:"absolute_failures"`
		Model            string `json:"model"`
	} `json:"t1"`
	Tier2 struct {
		LastRun          int64  `json:"last_run"`
		NextRun          int64  `json:"next_run"`
		Attempts         int64  `json:"attempts"`
		Failures         int64  `json:"failures"`
		AbsoluteFailures int64  `json:"absolute_failures"`
		Model            string `json:"model"`
	} `json:"t2"`
	SystemIdleTime  int64 `json:"system_idle_time"`
	TotalActiveTime int64 `json:"total_active_time"`
	TotalIdleTime   int64 `json:"total_idle_time"`
	TotalWasteTime  int64 `json:"total_waste_time"`
}

// GetGuardianStatusHandler returns the current timing status of the guardian worker.
func GetGuardianStatusHandler(redisClient *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if redisClient == nil {
			http.Error(w, "Redis not connected", http.StatusServiceUnavailable)
			return
		}

		ctx := context.Background()
		var status GuardianStatusResponse

		// Active Tier
		status.ActiveTier, _ = redisClient.Get(ctx, "guardian:active_tier").Result()

		// Tier 1
		lastT1TS, _ := redisClient.Get(ctx, "guardian:last_run:t1").Int64()
		status.Tier1.LastRun = lastT1TS
		status.Tier1.NextRun = lastT1TS + 1800 // 30 minutes
		status.Tier1.Model = "dex-guardian-t1"
		status.Tier1.Attempts, _ = redisClient.Get(ctx, "system:metrics:model:"+status.Tier1.Model+":attempts").Int64()
		status.Tier1.Failures, _ = redisClient.Get(ctx, "system:metrics:model:"+status.Tier1.Model+":failures").Int64()
		status.Tier1.AbsoluteFailures, _ = redisClient.Get(ctx, "system:metrics:model:"+status.Tier1.Model+":absolute_failures").Int64()

		// Tier 2
		lastT2TS, _ := redisClient.Get(ctx, "guardian:last_run:t2").Int64()
		status.Tier2.LastRun = lastT2TS
		status.Tier2.NextRun = lastT2TS + 900 // 15 minutes
		status.Tier2.Model = "dex-guardian-t2"
		status.Tier2.Attempts, _ = redisClient.Get(ctx, "system:metrics:model:"+status.Tier2.Model+":attempts").Int64()
		status.Tier2.Failures, _ = redisClient.Get(ctx, "system:metrics:model:"+status.Tier2.Model+":failures").Int64()
		status.Tier2.AbsoluteFailures, _ = redisClient.Get(ctx, "system:metrics:model:"+status.Tier2.Model+":absolute_failures").Int64()

		// System Idle Time (Current)
		lastEventTS, _ := redisClient.Get(ctx, "system:last_cognitive_event").Int64()
		if lastEventTS > 0 {
			now := time.Now().Unix()
			status.SystemIdleTime = now - lastEventTS
		}

		// Total Metrics
		active, _ := redisClient.Get(ctx, "system:metrics:total_active_seconds").Int64()
		waste, _ := redisClient.Get(ctx, "system:metrics:total_waste_seconds").Int64()
		idle, _ := redisClient.Get(ctx, "system:metrics:total_idle_seconds").Int64()

		status.TotalActiveTime = active
		status.TotalWasteTime = waste
		status.TotalIdleTime = idle

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(status); err != nil {
			log.Printf("Error encoding guardian status: %v", err)
		}
	}
}

// HandleUpdateGuardianStatus updates the guardian status (e.g., setting the active tier).
func HandleUpdateGuardianStatus(redisClient *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if redisClient == nil {
			http.Error(w, "Redis not connected", http.StatusServiceUnavailable)
			return
		}

		var req struct {
			ActiveTier string `json:"active_tier"`
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		ctx := context.Background()
		if err := redisClient.Set(ctx, "guardian:active_tier", req.ActiveTier, utils.DefaultTTL).Err(); err != nil {
			http.Error(w, fmt.Sprintf("Failed to update active tier: %v", err), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Guardian status updated successfully"))
	}
}

// RunGuardianHandler triggers immediate execution of guardian tiers.
func RunGuardianHandler(redisClient *redis.Client, triggerFunc func(int) ([]interface{}, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tierStr := r.URL.Query().Get("tier")
		tier, _ := strconv.Atoi(tierStr)

		results, err := triggerFunc(tier)
		if err != nil {
			http.Error(w, fmt.Sprintf("Guardian run failed: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(results)
	}
}

// ResetGuardianHandler resets the timers for guardian tiers.
func ResetGuardianHandler(redisClient *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if redisClient == nil {
			http.Error(w, "Redis not connected", http.StatusServiceUnavailable)
			return
		}

		ctx := context.Background()
		tier := r.URL.Query().Get("tier")

		if tier == "all" || tier == "t2" {
			redisClient.Set(ctx, "guardian:last_run:t2", 0, utils.DefaultTTL)
		}
		if tier == "all" || tier == "t1" {
			redisClient.Set(ctx, "guardian:last_run:t1", 0, utils.DefaultTTL)
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Guardian timers reset successfully"))
	}
}
