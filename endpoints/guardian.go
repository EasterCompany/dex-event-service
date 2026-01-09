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

// AgentStatusResponse returns the status of all agents and system state.
type AgentStatusResponse struct {
	Agents map[string]interface{} `json:"agents"`
	System struct {
		State     string `json:"state"`
		StateTime int64  `json:"state_time"`
		Metrics   struct {
			Active int64 `json:"total_active_time"`
			Idle   int64 `json:"total_idle_time"`
			Waste  int64 `json:"total_waste_time"`
		} `json:"metrics"`
	} `json:"system"`
}

// GetAgentStatusHandler returns the current timing status of the guardian worker and system state.
func GetAgentStatusHandler(redisClient *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if redisClient == nil {
			http.Error(w, "Redis not connected", http.StatusServiceUnavailable)
			return
		}

		ctx := context.Background()
		var status AgentStatusResponse
		status.Agents = make(map[string]interface{})

		// 1. Guardian Agent Specifics (Legacy structure for frontend compatibility)
		guardian := make(map[string]interface{})
		activeTier, _ := redisClient.Get(ctx, "guardian:active_tier").Result()
		guardian["active_tier"] = activeTier

		// Sentry
		lastSentryTS, _ := redisClient.Get(ctx, "guardian:last_run:sentry").Int64()
		sentryModel := "dex-guardian-t1"
		sentryAttempts, _ := redisClient.Get(ctx, "system:metrics:model:"+sentryModel+":attempts").Int64()
		sentryFailures, _ := redisClient.Get(ctx, "system:metrics:model:"+sentryModel+":failures").Int64()
		sentryAbsolute, _ := redisClient.Get(ctx, "system:metrics:model:"+sentryModel+":absolute_failures").Int64()
		guardian["sentry"] = map[string]interface{}{
			"last_run": lastSentryTS, "next_run": lastSentryTS + 1800, "model": sentryModel,
			"attempts": sentryAttempts, "failures": sentryFailures, "absolute_failures": sentryAbsolute,
		}

		// Alert Review (Imaginator)
		lastAlertReviewTS, _ := redisClient.Get(ctx, "imaginator:last_run:alert_review").Int64()
		alertReviewModel := "dex-imaginator-model"
		alertReviewAttempts, _ := redisClient.Get(ctx, "system:metrics:model:"+alertReviewModel+":attempts").Int64()
		alertReviewFailures, _ := redisClient.Get(ctx, "system:metrics:model:"+alertReviewModel+":failures").Int64()
		alertReviewAbsolute, _ := redisClient.Get(ctx, "system:metrics:model:"+alertReviewModel+":absolute_failures").Int64()
		guardian["alert_review"] = map[string]interface{}{
			"last_run": lastAlertReviewTS, "next_run": lastAlertReviewTS + 60, "model": alertReviewModel,
			"attempts": alertReviewAttempts, "failures": alertReviewFailures, "absolute_failures": alertReviewAbsolute,
		}

		// Protocol Aliases
		guardian["protocol_aliases"] = map[string]string{
			"sentry":       "Sentry",
			"alert_review": "Alert Review",
		}

		status.Agents["guardian"] = guardian

		// 1.1 Analyzer Agent Specifics
		analyzer := make(map[string]interface{})
		activeSynthesis, _ := redisClient.Get(ctx, "analyzer:active_tier").Result()
		analyzer["active_tier"] = activeSynthesis

		// Synthesis
		lastSynthesisTS, _ := redisClient.Get(ctx, "analyzer:last_run:synthesis").Int64()
		synthesisModel := "dex-master-model"
		synthesisAttempts, _ := redisClient.Get(ctx, "system:metrics:model:"+synthesisModel+":attempts").Int64()
		synthesisFailures, _ := redisClient.Get(ctx, "system:metrics:model:"+synthesisModel+":failures").Int64()
		synthesisAbsolute, _ := redisClient.Get(ctx, "system:metrics:model:"+synthesisModel+":absolute_failures").Int64()
		analyzer["synthesis"] = map[string]interface{}{
			"last_run": lastSynthesisTS, "next_run": lastSynthesisTS + 43200, "model": synthesisModel,
			"attempts": synthesisAttempts, "failures": synthesisFailures, "absolute_failures": synthesisAbsolute,
		}

		status.Agents["analyzer"] = analyzer

		// 2. System State & Metrics
		lastTransition, _ := redisClient.Get(ctx, "system:last_transition_ts").Int64()
		status.System.State, _ = redisClient.Get(ctx, "system:state").Result()
		if status.System.State == "" {
			status.System.State = "idle"
		}
		if lastTransition > 0 {
			status.System.StateTime = time.Now().Unix() - lastTransition
		}

		status.System.Metrics.Active, _ = redisClient.Get(ctx, "system:metrics:total_active_seconds").Int64()
		status.System.Metrics.Waste, _ = redisClient.Get(ctx, "system:metrics:total_waste_seconds").Int64()
		status.System.Metrics.Idle, _ = redisClient.Get(ctx, "system:metrics:total_idle_seconds").Int64()

		// 3. Flatten for frontend compatibility (Legacy structure)
		// The frontend expects the fields at the root of the object
		response := make(map[string]interface{})
		response["active_tier"] = guardian["active_tier"]
		response["sentry"] = guardian["sentry"]
		response["alert_review"] = guardian["alert_review"]
		response["protocol_aliases"] = guardian["protocol_aliases"]

		// Analyzer
		response["active_synthesis"] = analyzer["active_tier"]
		response["synthesis"] = analyzer["synthesis"]

		response["system_state"] = status.System.State
		response["system_state_time"] = status.System.StateTime
		response["total_active_time"] = status.System.Metrics.Active
		response["total_idle_time"] = status.System.Metrics.Idle
		response["total_waste_time"] = status.System.Metrics.Waste

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			log.Printf("Error encoding agent status: %v", err)
		}
	}
}

// GetAgentStatusSnapshot returns current status data without HTTP context
func GetAgentStatusSnapshot(redisClient *redis.Client) map[string]interface{} {
	if redisClient == nil {
		return nil
	}

	ctx := context.Background()

	// 1. Guardian Agent
	activeTier, _ := redisClient.Get(ctx, "guardian:active_tier").Result()
	lastSentryTS, _ := redisClient.Get(ctx, "guardian:last_run:sentry").Int64()
	sentryModel := "dex-guardian-t1"
	sentryAttempts, _ := redisClient.Get(ctx, "system:metrics:model:"+sentryModel+":attempts").Int64()
	sentryFailures, _ := redisClient.Get(ctx, "system:metrics:model:"+sentryModel+":failures").Int64()
	sentryAbsolute, _ := redisClient.Get(ctx, "system:metrics:model:"+sentryModel+":absolute_failures").Int64()

	lastAlertReviewTS, _ := redisClient.Get(ctx, "imaginator:last_run:alert_review").Int64()
	alertReviewModel := "dex-imaginator-model"
	alertReviewAttempts, _ := redisClient.Get(ctx, "system:metrics:model:"+alertReviewModel+":attempts").Int64()
	alertReviewFailures, _ := redisClient.Get(ctx, "system:metrics:model:"+alertReviewModel+":failures").Int64()
	alertReviewAbsolute, _ := redisClient.Get(ctx, "system:metrics:model:"+alertReviewModel+":absolute_failures").Int64()

	// 2. Analyzer Agent
	activeSynthesis, _ := redisClient.Get(ctx, "analyzer:active_tier").Result()
	lastSynthesisTS, _ := redisClient.Get(ctx, "analyzer:last_run:synthesis").Int64()
	synthesisModel := "dex-master-model"
	synthesisAttempts, _ := redisClient.Get(ctx, "system:metrics:model:"+synthesisModel+":attempts").Int64()
	synthesisFailures, _ := redisClient.Get(ctx, "system:metrics:model:"+synthesisModel+":failures").Int64()
	synthesisAbsolute, _ := redisClient.Get(ctx, "system:metrics:model:"+synthesisModel+":absolute_failures").Int64()

	// 3. System State & Metrics
	lastTransition, _ := redisClient.Get(ctx, "system:last_transition_ts").Int64()
	state, _ := redisClient.Get(ctx, "system:state").Result()
	if state == "" {
		state = "idle"
	}
	var stateTime int64
	if lastTransition > 0 {
		stateTime = time.Now().Unix() - lastTransition
	}

	activeSecs, _ := redisClient.Get(ctx, "system:metrics:total_active_seconds").Int64()
	wasteSecs, _ := redisClient.Get(ctx, "system:metrics:total_waste_seconds").Int64()
	idleSecs, _ := redisClient.Get(ctx, "system:metrics:total_idle_seconds").Int64()

	// 4. Flatten for frontend compatibility
	response := make(map[string]interface{})
	response["active_tier"] = activeTier
	response["sentry"] = map[string]interface{}{
		"last_run": lastSentryTS, "next_run": lastSentryTS + 1800, "model": sentryModel,
		"attempts": sentryAttempts, "failures": sentryFailures, "absolute_failures": sentryAbsolute,
	}
	response["alert_review"] = map[string]interface{}{
		"last_run": lastAlertReviewTS, "next_run": lastAlertReviewTS + 60, "model": alertReviewModel,
		"attempts": alertReviewAttempts, "failures": alertReviewFailures, "absolute_failures": alertReviewAbsolute,
	}
	response["active_synthesis"] = activeSynthesis
	response["synthesis"] = map[string]interface{}{
		"last_run": lastSynthesisTS, "next_run": lastSynthesisTS + 43200, "model": synthesisModel,
		"attempts": synthesisAttempts, "failures": synthesisFailures, "absolute_failures": synthesisAbsolute,
	}
	response["protocol_aliases"] = map[string]string{
		"sentry":       "Sentry",
		"alert_review": "Alert Review",
		"synthesis":    "Synthesis",
	}
	response["system_state"] = state
	response["system_state_time"] = stateTime
	response["total_active_time"] = activeSecs
	response["total_idle_time"] = idleSecs
	response["total_waste_time"] = wasteSecs

	return response
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

// RunGuardianHandler triggers immediate execution of guardian protocols.
func RunGuardianHandler(redisClient *redis.Client, triggerFunc func(int) ([]interface{}, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tierStr := r.URL.Query().Get("tier")
		if tierStr == "" {
			tierStr = r.URL.Query().Get("protocol")
		}
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

// ResetGuardianHandler resets the timers for guardian protocols.
func ResetGuardianHandler(redisClient *redis.Client) http.HandlerFunc {
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

		if tier == "" || tier == "all" || tier == "alert_review" {
			redisClient.Set(ctx, "imaginator:last_run:alert_review", 0, utils.DefaultTTL)
		}
		if tier == "" || tier == "all" || tier == "sentry" {
			redisClient.Set(ctx, "guardian:last_run:sentry", 0, utils.DefaultTTL)
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Guardian protocols reset successfully"))
	}
}
