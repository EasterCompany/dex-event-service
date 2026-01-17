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

// ProtocolStats holds execution metrics.
type ProtocolStats struct {
	Runs     int64 `json:"runs"`
	Failures int64 `json:"failures"`
	Aborted  int64 `json:"aborted"`
}

// ProtocolStatus holds the dynamic state of a protocol.
type ProtocolStatus struct {
	Status      string        `json:"status"` // "Ready", "Working", "Cooldown"
	Cooldown    int64         `json:"cooldown"`
	LastRun     int64         `json:"last_run"`
	NextRun     int64         `json:"next_run"`
	Model       string        `json:"model"`
	Stats       ProtocolStats `json:"stats"`
	Description string        `json:"description,omitempty"`
}

// AgentState holds the state of an entire agent.
type AgentState struct {
	ActiveProtocol string                    `json:"active_protocol,omitempty"` // The key/slug of the currently running protocol
	Protocols      map[string]ProtocolStatus `json:"protocols"`
}

// SystemMetrics holds global system counters.
type SystemMetrics struct {
	Active int64 `json:"total_active_time"`
	Idle   int64 `json:"total_idle_time"`
	Waste  int64 `json:"total_waste_time"`
}

// SystemState holds global system state.
type SystemState struct {
	State     string        `json:"state"`
	StateTime int64         `json:"state_time"`
	Metrics   SystemMetrics `json:"metrics"`
}

// AgentStatusResponse is the top-level response structure.
type AgentStatusResponse struct {
	Agents map[string]AgentState `json:"agents"`
	System SystemState           `json:"system"`
}

// Helper to calculate protocol status
func calculateProtocolStatus(ctx context.Context, rdb *redis.Client, agentActiveProtocol string, protocolKey string, interval int64, modelName string, redisPrefix string) ProtocolStatus {
	lastRun, _ := rdb.Get(ctx, fmt.Sprintf("%s:last_run:%s", redisPrefix, protocolKey)).Int64()
	nextRun := lastRun + interval
	now := time.Now().Unix()

	attempts, _ := rdb.Get(ctx, fmt.Sprintf("system:metrics:model:%s:attempts", modelName)).Int64()
	failures, _ := rdb.Get(ctx, fmt.Sprintf("system:metrics:model:%s:failures", modelName)).Int64()
	aborted, _ := rdb.Get(ctx, fmt.Sprintf("system:metrics:model:%s:absolute_failures", modelName)).Int64()

	status := "Cooldown"
	cooldown := int64(0)

	if agentActiveProtocol == protocolKey {
		status = "Working"
	} else if now >= nextRun {
		status = "Ready"
	} else {
		cooldown = nextRun - now
	}

	return ProtocolStatus{
		Status:   status,
		Cooldown: cooldown,
		LastRun:  lastRun,
		NextRun:  nextRun,
		Model:    modelName,
		Stats: ProtocolStats{
			Runs:     attempts,
			Failures: failures,
			Aborted:  aborted,
		},
	}
}

// GetAgentStatusHandler returns the status of all agents and system state.
func GetAgentStatusHandler(redisClient *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if redisClient == nil {
			http.Error(w, "Redis not connected", http.StatusServiceUnavailable)
			return
		}

		response := buildAgentStatus(redisClient)

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			log.Printf("Error encoding agent status: %v", err)
		}
	}
}

func buildAgentStatus(rdb *redis.Client) AgentStatusResponse {
	ctx := context.Background()
	resp := AgentStatusResponse{
		Agents: make(map[string]AgentState),
	}

	// --- Guardian Agent ---
	guardianActive, _ := rdb.Get(ctx, "guardian:active_tier").Result()
	guardianProtocols := make(map[string]ProtocolStatus)

	// Sentry (Every 30 mins = 1800s)
	guardianProtocols["sentry"] = calculateProtocolStatus(
		ctx, rdb, guardianActive, "sentry", 1800, "dex-guardian-sentry", "guardian",
	)

	resp.Agents["guardian"] = AgentState{
		ActiveProtocol: guardianActive,
		Protocols:      guardianProtocols,
	}

	// --- Imaginator Agent ---
	// Imaginator shares the 'guardian:active_tier' key logic currently or has its own?
	// The alert_review triggers set "guardian:active_tier" to "alert_review".
	// So we use guardianActive for now, but conceptually it's Imaginator.
	imaginatorProtocols := make(map[string]ProtocolStatus)
	imaginatorProtocols["alert_review"] = calculateProtocolStatus(
		ctx, rdb, guardianActive, "alert_review", 60, "dex-imaginator-model", "imaginator",
	)

	resp.Agents["imaginator"] = AgentState{
		ActiveProtocol: guardianActive, // Temporarily shared
		Protocols:      imaginatorProtocols,
	}

	// --- Analyzer Agent ---
	analyzerActive, _ := rdb.Get(ctx, "analyzer:active_tier").Result()
	analyzerProtocols := make(map[string]ProtocolStatus)

	// Synthesis (Every 12 hours = 43200s)
	analyzerProtocols["synthesis"] = calculateProtocolStatus(
		ctx, rdb, analyzerActive, "synthesis", 43200, "dex-master-model", "analyzer",
	)

	resp.Agents["analyzer"] = AgentState{
		ActiveProtocol: analyzerActive,
		Protocols:      analyzerProtocols,
	}

	// --- Fabricator Agent ---
	fabricatorActive, _ := rdb.Get(ctx, "fabricator:active_tier").Result()
	fabricatorProtocols := make(map[string]ProtocolStatus)

	// Construction (Every 60s check)
	fabricatorProtocols["construction"] = calculateProtocolStatus(
		ctx, rdb, fabricatorActive, "construction", 60, "gemini-cli-yolo", "fabricator",
	)

	resp.Agents["fabricator"] = AgentState{
		ActiveProtocol: fabricatorActive,
		Protocols:      fabricatorProtocols,
	}

	// --- System State ---
	lastTransition, _ := rdb.Get(ctx, "system:last_transition_ts").Int64()
	state, _ := rdb.Get(ctx, "system:state").Result()
	if state == "" {
		state = "idle"
	}
	var stateTime int64
	if lastTransition > 0 {
		stateTime = time.Now().Unix() - lastTransition
	}

	activeSecs, _ := rdb.Get(ctx, "system:metrics:total_active_seconds").Int64()
	wasteSecs, _ := rdb.Get(ctx, "system:metrics:total_waste_seconds").Int64()
	idleSecs, _ := rdb.Get(ctx, "system:metrics:total_idle_seconds").Int64()

	resp.System = SystemState{
		State:     state,
		StateTime: stateTime,
		Metrics: SystemMetrics{
			Active: activeSecs,
			Idle:   idleSecs,
			Waste:  wasteSecs,
		},
	}

	return resp
}

// GetAgentStatusSnapshot returns current status data (Internal Use)
func GetAgentStatusSnapshot(redisClient *redis.Client) map[string]interface{} {
	if redisClient == nil {
		return nil
	}
	// Convert the typed struct to a generic map for legacy compatibility if needed,
	// or just return the struct as map (via JSON marshaling intermediate or direct reflection).
	// For simplicity and performance in Go, we'll just re-call buildAgentStatus and map it manually
	// or assume the caller handles the struct if we changed the signature.
	// However, the interface expects map[string]interface{}.

	data := buildAgentStatus(redisClient)

	// Quick marshal/unmarshal hack to convert struct to map[string]interface{}
	// This ensures consistency without rewriting the mapping logic twice.
	var mapResult map[string]interface{}
	bytes, _ := json.Marshal(data)
	_ = json.Unmarshal(bytes, &mapResult)

	return mapResult
}

// HandleUpdateGuardianStatus updates the guardian status.
// Refactor: Should be HandleUpdateAgentStatus eventually, but keeping route compat.
func HandleUpdateGuardianStatus(redisClient *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if redisClient == nil {
			http.Error(w, "Redis not connected", http.StatusServiceUnavailable)
			return
		}

		var req struct {
			ActiveTier string `json:"active_tier"`
			Agent      string `json:"agent"` // Optional: "guardian", "analyzer"
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		ctx := context.Background()
		agent := req.Agent
		if agent == "" {
			agent = "guardian" // Default to guardian for legacy
		}

		key := fmt.Sprintf("%s:active_tier", agent)

		if err := redisClient.Set(ctx, key, req.ActiveTier, utils.DefaultTTL).Err(); err != nil {
			http.Error(w, fmt.Sprintf("Failed to update active tier: %v", err), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Agent status updated successfully"))
	}
}

// RunGuardianHandler triggers immediate execution of protocols.
func RunGuardianHandler(redisClient *redis.Client, triggerFunc func(int) ([]interface{}, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tierStr := r.URL.Query().Get("tier")
		if tierStr == "" {
			tierStr = r.URL.Query().Get("protocol")
		}

		// Map strings to legacy integer tiers if necessary, or pass string to triggerFunc
		// Assuming triggerFunc still takes int (Legacy).
		// Ideally we update triggerFunc to take a string protocol name.

		tier, _ := strconv.Atoi(tierStr)
		// If 0, it might be a string like "sentry" which Atoi fails on.
		// For now, we assume the caller sends the numeric tier or we map it.
		// Map text to tier IDs:
		if tier == 0 && tierStr != "0" {
			switch tierStr {
			case "sentry":
				tier = 1
			case "architect":
				tier = 2
				// Add others as needed
			}
		}

		results, err := triggerFunc(tier)
		if err != nil {
			http.Error(w, fmt.Sprintf("Guardian run failed: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(results)
	}
}

// ResetGuardianHandler resets the timers for protocols.
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

		// Reset logic
		if tier == "alert_review" || tier == "all" {
			redisClient.Set(ctx, "imaginator:last_run:alert_review", 0, utils.DefaultTTL)
		}
		if tier == "sentry" || tier == "all" {
			redisClient.Set(ctx, "guardian:last_run:sentry", 0, utils.DefaultTTL)
		}
		// TODO: Add Synthesis reset if needed, though usually handled by analyzer endpoints

		if tier == "construction" || tier == "all" {
			redisClient.Set(ctx, "fabricator:last_run:construction", 0, utils.DefaultTTL)
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Protocols reset successfully"))
	}
}

// RunFabricatorHandler triggers immediate execution of protocols.
func RunFabricatorHandler(redisClient *redis.Client, triggerFunc func() error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := triggerFunc(); err != nil {
			http.Error(w, fmt.Sprintf("Fabricator run failed: %v", err), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status": "triggered"}`))
	}
}

// HandlePauseSystem pauses all system agents.
func HandlePauseSystem(redisClient *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := context.Background()
		now := time.Now().Unix()

		// 1. Capture current state to restore it later
		prevState, _ := redisClient.Get(ctx, "system:state").Result()
		if prevState == "" || prevState == "paused" {
			prevState = "idle"
		}

		// 2. Set Pause Metadata
		redisClient.Set(ctx, "system:pre_pause_state", prevState, 0)
		redisClient.Set(ctx, "system:pause_start_ts", now, 0)

		// 3. Set System Paused
		redisClient.Set(ctx, "system:is_paused", "true", 0)
		redisClient.Set(ctx, "system:state", "paused", 0)

		// 4. Set Cognitive Lock to PAUSED (forcibly)
		redisClient.Set(ctx, "system:cognitive_lock", "PAUSED", 0)

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("System paused"))
	}
}

// HandleResumeSystem resumes all system agents.
func HandleResumeSystem(redisClient *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := context.Background()
		now := time.Now().Unix()

		// 1. Calculate Pause Duration
		pauseStart, _ := redisClient.Get(ctx, "system:pause_start_ts").Int64()
		if pauseStart > 0 {
			pauseDuration := now - pauseStart

			// 2. Shift the last transition timestamp forward by the pause duration
			// This "freezes" the timer in the eyes of the duration calculator.
			lastTransition, _ := redisClient.Get(ctx, "system:last_transition_ts").Int64()
			if lastTransition > 0 {
				redisClient.Set(ctx, "system:last_transition_ts", lastTransition+pauseDuration, 0)
			}
		}

		// 3. Restore previous state
		prevState, _ := redisClient.Get(ctx, "system:pre_pause_state").Result()
		if prevState == "" {
			prevState = "idle"
		}
		redisClient.Set(ctx, "system:state", prevState, 0)

		// 4. Cleanup
		redisClient.Del(ctx, "system:is_paused")
		redisClient.Del(ctx, "system:pause_start_ts")
		redisClient.Del(ctx, "system:pre_pause_state")

		// 5. Del Cognitive Lock if it is "PAUSED"
		val, _ := redisClient.Get(ctx, "system:cognitive_lock").Result()
		if val == "PAUSED" {
			redisClient.Del(ctx, "system:cognitive_lock")
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("System resumed"))
	}
}
