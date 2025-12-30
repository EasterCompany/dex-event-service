package guardian

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/EasterCompany/dex-event-service/internal/agent"
	"github.com/EasterCompany/dex-event-service/internal/discord"
	"github.com/EasterCompany/dex-event-service/internal/ollama"
	"github.com/EasterCompany/dex-event-service/internal/web"
	"github.com/EasterCompany/dex-event-service/templates"
	"github.com/EasterCompany/dex-event-service/types"
	"github.com/EasterCompany/dex-event-service/utils"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	HandlerName = "guardian-handler"
)

type GuardianHandler struct {
	agent.BaseAgent
	Config        agent.AgentConfig
	DiscordClient *discord.Client
	WebClient     *web.Client
	stopChan      chan struct{}
	ctx           context.Context
	cancel        context.CancelFunc
}

func NewGuardianHandler(redis *redis.Client, ollama *ollama.Client, discord *discord.Client, web *web.Client, options interface{}) *GuardianHandler {
	ctx, cancel := context.WithCancel(context.Background())
	return &GuardianHandler{
		BaseAgent: agent.BaseAgent{
			RedisClient:  redis,
			OllamaClient: ollama,
			ChatManager:  utils.NewChatContextManager(redis),
			StopTokens:   []string{"<NO_ALERT/>", "<NO_BLUEPRINT/>", "<NO_ISSUES/>"},
		},
		Config: agent.AgentConfig{
			Name:      "Guardian",
			ProcessID: "system-guardian",
			Models: map[string]string{
				"t1": "dex-guardian-t1",
				"t2": "dex-guardian-t2",
			},
			ProtocolAliases: map[string]string{
				"t1": "Sentry",
				"t2": "Architect",
			},
			Cooldowns: map[string]int{
				"t1": 1800,
				"t2": 1800,
			},
			IdleRequirement: 300,
			DateTimeAware:   true,
			EnforceMarkdown: true,
			RequiredSections: []string{
				"Summary", "Content", "Priority", "Category", "Affected",
			},
		},
		DiscordClient: discord,
		WebClient:     web,
		ctx:           ctx,
		cancel:        cancel,
	}
}

func (h *GuardianHandler) Init(ctx context.Context) error {
	h.stopChan = make(chan struct{})
	go h.runWorker()
	log.Printf("[%s] Background worker started.", HandlerName)
	return nil
}

func (h *GuardianHandler) Close() error {
	if h.cancel != nil {
		h.cancel()
	}
	if h.stopChan != nil {
		close(h.stopChan)
	}
	utils.ClearProcess(context.Background(), h.RedisClient, h.DiscordClient, h.Config.ProcessID)
	return nil
}

func (h *GuardianHandler) GetConfig() agent.AgentConfig {
	return h.Config
}

func (h *GuardianHandler) Run(ctx context.Context) ([]agent.AnalysisResult, error) {
	return h.PerformAnalysis(ctx, 0)
}

func (h *GuardianHandler) runWorker() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-h.stopChan:
			return
		case <-ticker.C:
			h.checkAndAnalyze()
		}
	}
}

func (h *GuardianHandler) checkAndAnalyze() {
	ctx := h.ctx
	now := time.Now().Unix()

	// 1. System Idle Requirement
	lastCognitiveEvent, _ := h.RedisClient.Get(ctx, "system:last_cognitive_event").Int64()
	if now-lastCognitiveEvent < int64(h.Config.IdleRequirement) {
		return
	}

	// 2. No ongoing processes (True Busy Check)
	if h.IsActuallyBusy(ctx, h.Config.ProcessID) {
		return
	}

	// 3. Already running check
	activeTier, _ := h.RedisClient.Get(ctx, "guardian:active_tier").Result()
	if activeTier != "" {
		return
	}

	// 4. Busy Count Cleanup
	h.CleanupBusyCount(ctx)

	// 5. Cooldown checks
	lastT1, _ := h.RedisClient.Get(ctx, "guardian:last_run:t1").Int64()
	lastT2, _ := h.RedisClient.Get(ctx, "guardian:last_run:t2").Int64()

	if (now-lastT1 < int64(h.Config.Cooldowns["t1"])) || (now-lastT2 < int64(h.Config.Cooldowns["t2"])) {
		return
	}

	// Trigger full analysis
	_, err := h.PerformAnalysis(ctx, 0)
	if err != nil {
		log.Printf("[%s] Automated analysis failed: %v", HandlerName, err)
		return
	}
}

func (h *GuardianHandler) PerformAnalysis(ctx context.Context, tier int) ([]agent.AnalysisResult, error) {
	log.Printf("[%s] Starting Guardian Analysis (Tier: %d)", HandlerName, tier)

	defer h.RedisClient.Del(ctx, "guardian:active_tier")
	defer utils.ClearProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID)

	if tier == 0 || tier == 1 {
		h.runSystemTests(ctx)
	}

	// Tier 1: Technical Sentry
	var t1Results []agent.AnalysisResult
	var t1EventIDs []string
	if tier == 0 || tier == 1 {
		h.RedisClient.Set(ctx, "guardian:active_tier", "t1", utils.DefaultTTL)
		utils.ReportProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID, "Sentry Protocol (T1)")

		input := h.gatherContext(ctx, "t1", nil)
		results, err := h.RunCognitiveLoop(ctx, h, "t1", h.Config.Models["t1"], "t1", "", input, 1)

		if err == nil {
			utils.RecordProcessOutcome(ctx, h.RedisClient, h.Config.ProcessID, "success")
			for i := range results {
				results[i].Type = "alert"
				id, _ := h.emitResult(ctx, results[i])
				t1EventIDs = append(t1EventIDs, id)
			}
			t1Results = results
			h.RedisClient.Set(ctx, "guardian:last_run:t1", time.Now().Unix(), 0)
		} else {
			utils.RecordProcessOutcome(ctx, h.RedisClient, h.Config.ProcessID, "error")
		}
	}

	// Determine if Tier 2 should run
	// Run if explicitly requested (tier 2) OR if full cycle (tier 0) AND Tier 1 found issues
	shouldRunT2 := (tier == 2) || (tier == 0 && len(t1Results) > 0)

	// Tier 2: Architect
	if shouldRunT2 {
		h.RedisClient.Set(ctx, "guardian:active_tier", "t2", utils.DefaultTTL)
		utils.ReportProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID, "Architect Protocol (T2)")

		input := h.gatherContext(ctx, "t2", t1Results)
		t2Results, err := h.RunCognitiveLoop(ctx, h, "t2", h.Config.Models["t2"], "t2", "", input, 1)

		if err == nil {
			utils.RecordProcessOutcome(ctx, h.RedisClient, h.Config.ProcessID, "success")
			for i := range t2Results {
				t2Results[i].Type = "blueprint"
				t2Results[i].Body = t2Results[i].Summary
				t2Results[i].SourceEventIDs = t1EventIDs
				_, _ = h.emitResult(ctx, t2Results[i])
			}
			h.RedisClient.Set(ctx, "guardian:last_run:t2", time.Now().Unix(), 0)
		} else {
			utils.RecordProcessOutcome(ctx, h.RedisClient, h.Config.ProcessID, "error")
		}
	}
	return nil, nil // Results already emitted individually
}

// ValidateLogic implements protocol-specific logical checks.
func (h *GuardianHandler) ValidateLogic(res agent.AnalysisResult) []agent.Correction {
	var corrections []agent.Correction

	// 1. Content Quality
	if len(res.Summary) < 20 {
		corrections = append(corrections, agent.Correction{
			Type: "LOGIC", Guidance: "The summary is too brief. Provide a high-fidelity one-sentence description of the issue or proposal.", Mandatory: true,
		})
	}

	// 2. Blueprint Specifics
	if res.Type == "blueprint" {
		if len(res.ImplementationPath) == 0 {
			corrections = append(corrections, agent.Correction{
				Type: "LOGIC", Guidance: "This is a [BLUEPRINT]. You MUST provide a 'Proposed Steps' section with at least one technical step to resolve the issue.", Mandatory: true,
			})
		}
	}

	return corrections
}

func (h *GuardianHandler) gatherContext(ctx context.Context, tier string, previousResults []agent.AnalysisResult) string {

	status, _ := h.fetchSystemStatus(ctx)
	logs, _ := h.fetchRecentLogs(ctx)
	tests, _ := h.fetchTestResults(ctx)
	cliHelp, _ := h.fetchCLICapabilities(ctx)

	var extra string
	if tier == "t1" {
		events, _ := h.fetchEventsForAnalysis(ctx)
		systemInfo, _ := h.fetchSystemHardware(ctx)
		extra = fmt.Sprintf("\n\n### HARDWARE & CONTEXT\n%s\n\n### RECENT EVENTS:\n%s", systemInfo, events)
	} else if tier == "t2" && len(previousResults) > 0 {
		t1JSON, _ := json.Marshal(previousResults)
		extra = "\n\n### RECENT TIER 1 REPORTS:\n" + string(t1JSON)
	}

	return fmt.Sprintf("### SYSTEM STATUS\n%s\n\n### CLI CAPABILITIES\n%s\n\n### RECENT LOGS\n%s\n\n### TEST RESULTS\n%s%s",
		status, cliHelp, logs, tests, extra)
}

// ... Context fetching methods remain largely similar but return strings for cleaner injection ...

func (h *GuardianHandler) emitResult(ctx context.Context, res agent.AnalysisResult) (string, error) {
	eventID := uuid.New().String()
	timestamp := time.Now().Unix()
	payload := map[string]interface{}{
		"title": res.Title, "priority": res.Priority, "category": res.Category,
		"body": res.Body, "related_event_ids": res.RelatedEventIDs,
		"source_event_ids": res.SourceEventIDs, "read": false,
	}

	var eventType string
	if res.Type == "blueprint" {
		eventType = string(types.EventTypeSystemBlueprintGenerated)
		payload["blueprint"] = true
		payload["summary"] = res.Summary
		payload["content"] = res.Content
		payload["affected_services"] = res.AffectedServices
		payload["implementation_path"] = res.ImplementationPath
	} else {
		eventType = string(types.EventTypeSystemNotificationGenerated)
		if res.Type == "alert" || res.Priority == "high" || res.Priority == "critical" {
			payload["alert"] = true
		}
	}
	payload["type"] = eventType
	eventJSON, _ := json.Marshal(payload)
	event := types.Event{ID: eventID, Service: HandlerName, Event: eventJSON, Timestamp: timestamp}
	fullJSON, _ := json.Marshal(event)

	pipe := h.RedisClient.Pipeline()
	// 1. Store main record
	pipe.Set(ctx, "event:"+eventID, fullJSON, utils.DefaultTTL)

	// 2. Add to global timeline (Discovery)
	pipe.ZAdd(ctx, "events:timeline", redis.Z{Score: float64(timestamp), Member: eventID})

	// 3. Add to service timeline
	pipe.ZAdd(ctx, "events:service:"+HandlerName, redis.Z{Score: float64(timestamp), Member: eventID})

	// 4. Add to type timeline (Alerts/Workspaces/etc)
	pipe.ZAdd(ctx, "events:type:"+eventType, redis.Z{Score: float64(timestamp), Member: eventID})

	_, err := pipe.Exec(ctx)
	return eventID, err
}

func (h *GuardianHandler) runSystemTests(ctx context.Context) {
	h.RedisClient.Set(ctx, "guardian:active_tier", "tests", utils.DefaultTTL)
	utils.ReportProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID, "Running System Tests")
	dexPath := h.getDexBinaryPath()
	cmd := exec.CommandContext(ctx, dexPath, "test")
	_ = cmd.Run()
}

func (h *GuardianHandler) getDexBinaryPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "dex"
	}
	return filepath.Join(home, "Dexter", "bin", "dex")
}

func (h *GuardianHandler) fetchEventsForAnalysis(ctx context.Context) (string, error) {
	eventIDs, _ := h.RedisClient.ZRevRange(ctx, "events:timeline", 0, 50).Result()
	var lines []string
	for _, id := range eventIDs {
		val, _ := h.RedisClient.Get(ctx, "event:"+id).Result()
		var e types.Event
		if err := json.Unmarshal([]byte(val), &e); err == nil {
			if e.Service == "dex-discord-service" {
				continue
			}
			var ed map[string]interface{}
			_ = json.Unmarshal(e.Event, &ed)
			lines = append(lines, templates.FormatEventAsText(ed["type"].(string), ed, e.Service, e.Timestamp, 0, "UTC", "en"))
		}
	}
	return strings.Join(lines, "\n"), nil
}

func (h *GuardianHandler) fetchSystemStatus(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, h.getDexBinaryPath(), "status")
	out, _ := cmd.CombinedOutput()
	return utils.StripANSI(string(out)), nil
}

func (h *GuardianHandler) fetchSystemHardware(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, h.getDexBinaryPath(), "system")
	out, _ := cmd.CombinedOutput()
	return utils.StripANSI(string(out)), nil
}

func (h *GuardianHandler) fetchRecentLogs(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, h.getDexBinaryPath(), "logs")
	out, _ := cmd.CombinedOutput()
	return utils.StripANSI(string(out)), nil
}

func (h *GuardianHandler) fetchTestResults(ctx context.Context) (string, error) {
	eventIDs, _ := h.RedisClient.ZRevRange(ctx, "events:timeline", 0, 250).Result()
	var tests []string
	seen := make(map[string]bool)
	for _, id := range eventIDs {
		data, _ := h.RedisClient.Get(ctx, "event:"+id).Result()
		var e types.Event
		if err := json.Unmarshal([]byte(data), &e); err == nil {
			var ed map[string]interface{}
			_ = json.Unmarshal(e.Event, &ed)
			if ed["type"] == string(types.EventTypeSystemTestCompleted) {
				svc, _ := ed["service_name"].(string)
				if svc != "" && !seen[svc] {
					tests = append(tests, fmt.Sprintf("[%s] %s", svc, ed["duration"]))
					seen[svc] = true
				}
			}
		}
	}
	return strings.Join(tests, "\n"), nil
}

func (h *GuardianHandler) fetchCLICapabilities(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, h.getDexBinaryPath(), "help")
	out, _ := cmd.CombinedOutput()
	return utils.StripANSI(string(out)), nil
}
