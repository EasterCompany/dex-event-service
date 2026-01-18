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

	// --- Courier Agent ---
	courierActive, _ := rdb.Get(ctx, "courier:active_tier").Result()
	courierProtocols := make(map[string]ProtocolStatus)

	// Researcher (5 min cooldown handled by calculateProtocolStatus)
	courierProtocols["researcher"] = calculateProtocolStatus(
		ctx, rdb, courierActive, "researcher", 300, "dex-scraper-model", "courier",
	)

	resp.Agents["courier"] = AgentState{
		ActiveProtocol: courierActive,
		Protocols:      courierProtocols,
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

// ResetAgentHandler resets the timers for any specific protocol.
func ResetAgentHandler(redisClient *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if redisClient == nil {
			http.Error(w, "Redis not connected", http.StatusServiceUnavailable)
			return
		}

		ctx := context.Background()
		query := r.URL.Query()
		protocol := query.Get("protocol")
		if protocol == "" {
			protocol = query.Get("tier")
		}

		// 1. Map protocols to their Redis keys
		// researcher -> courier:last_run:researcher
		// sentry -> guardian:last_run:sentry
		// alert_review -> imaginator:last_run:alert_review
		// construction -> fabricator:last_run:construction
		// synthesis -> analyzer:last_run:synthesis

		switch protocol {
		case "researcher":
			redisClient.Set(ctx, "courier:last_run:researcher", 0, utils.DefaultTTL)
			// Reset the cognitive idle timer as well, so it triggers immediately if conditions met
			redisClient.Set(ctx, "system:last_cognitive_event", 0, 0)
		case "sentry":
			redisClient.Set(ctx, "guardian:last_run:sentry", 0, utils.DefaultTTL)
		case "alert_review":
			redisClient.Set(ctx, "imaginator:last_run:alert_review", 0, utils.DefaultTTL)
		case "construction":
			redisClient.Set(ctx, "fabricator:last_run:construction", 0, utils.DefaultTTL)
		case "synthesis":
			redisClient.Set(ctx, "analyzer:last_run:synthesis", 0, utils.DefaultTTL)
			// Clear per-user cooldowns
			iter := redisClient.Scan(ctx, 0, "agent:Analyzer:cooldown:*", 0).Iterator()
			for iter.Next(ctx) {
				redisClient.Del(ctx, iter.Val())
			}
		case "all":
			// Reset everything
			redisClient.Set(ctx, "courier:last_run:researcher", 0, utils.DefaultTTL)
			redisClient.Set(ctx, "guardian:last_run:sentry", 0, utils.DefaultTTL)
			redisClient.Set(ctx, "imaginator:last_run:alert_review", 0, utils.DefaultTTL)
			redisClient.Set(ctx, "fabricator:last_run:construction", 0, utils.DefaultTTL)
			redisClient.Set(ctx, "analyzer:last_run:synthesis", 0, utils.DefaultTTL)
			redisClient.Set(ctx, "system:last_cognitive_event", 0, 0)

			// Clear per-user cooldowns
			iter := redisClient.Scan(ctx, 0, "agent:Analyzer:cooldown:*", 0).Iterator()
			for iter.Next(ctx) {
				redisClient.Del(ctx, iter.Val())
			}
		default:
			http.Error(w, fmt.Sprintf("Unknown protocol: %s", protocol), http.StatusBadRequest)
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "Protocol '%s' reset successfully", protocol)
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

		// 1. Verify current state is idle
		state, _ := redisClient.Get(ctx, "system:state").Result()
		if state != "idle" {
			http.Error(w, "System can only be paused when in 'idle' state", http.StatusForbidden)
			return
		}

		// 2. Add current idle duration to total metrics
		lastTransition, _ := redisClient.Get(ctx, "system:last_transition_ts").Int64()
		if lastTransition > 0 {
			idleDuration := now - lastTransition
			if idleDuration > 0 {
				redisClient.IncrBy(ctx, "system:metrics:total_idle_seconds", idleDuration)
			}
		}

		// 3. Set System to Paused and Reset Timer (to track pause duration)
		redisClient.Set(ctx, "system:is_paused", "true", 0)
		redisClient.Set(ctx, "system:state", "paused", 0)
		redisClient.Set(ctx, "system:last_transition_ts", now, 0)

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

		// 1. Reset State to Idle and Reset Timer (start new idle period)
		redisClient.Set(ctx, "system:state", "idle", 0)
		redisClient.Set(ctx, "system:last_transition_ts", now, 0)

		// 2. Cleanup Pause Flags
		redisClient.Del(ctx, "system:is_paused")

		// 3. Del Cognitive Lock if it is "PAUSED"
		val, _ := redisClient.Get(ctx, "system:cognitive_lock").Result()
		if val == "PAUSED" {
			redisClient.Del(ctx, "system:cognitive_lock")
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("System resumed"))
	}
}
