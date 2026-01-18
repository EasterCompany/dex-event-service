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
				"sentry": "dex-guardian-sentry",
			},
			ProtocolAliases: map[string]string{
				"sentry": "Sentry",
			},
			Cooldowns: map[string]int{
				"sentry": 3600,
			},
			IdleRequirement: 300,
			DateTimeAware:   true,
			EnforceMarkdown: true,
			RequiredSections: []string{
				"Summary", "Content", "Priority", "Category", "Related",
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

func (h *GuardianHandler) Run(ctx context.Context) ([]agent.AnalysisResult, string, error) {
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

	// 1.5 System Pause Check
	if utils.IsSystemPaused(ctx, h.RedisClient) {
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
	lastSentry, _ := h.RedisClient.Get(ctx, "guardian:last_run:sentry").Int64()

	if now-lastSentry < int64(h.Config.Cooldowns["sentry"]) {
		return
	}

	// Trigger full analysis
	_, _, err := h.PerformAnalysis(ctx, 0)
	if err != nil {
		log.Printf("[%s] Automated analysis failed: %v", HandlerName, err)
		return
	}
}

func (h *GuardianHandler) PerformAnalysis(ctx context.Context, tier int) ([]agent.AnalysisResult, string, error) {
	log.Printf("[%s] Starting Guardian Analysis (Tier: %d)", HandlerName, tier)

	// Enforce global sequential execution
	utils.AcquireCognitiveLock(ctx, h.RedisClient, h.Config.Name, h.Config.ProcessID, h.DiscordClient)
	defer utils.ReleaseCognitiveLock(ctx, h.RedisClient, h.Config.Name)

	defer h.RedisClient.Del(ctx, "guardian:active_tier")
	defer utils.ClearProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID)

	var lastAuditID string

	// Tier 1: Technical Sentry
	var sentryResults []agent.AnalysisResult
	if tier == 0 || tier == 1 {
		h.RedisClient.Set(ctx, "guardian:active_tier", "sentry", utils.DefaultTTL)
		utils.ReportProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID, "Sentry Protocol")

		input := h.gatherContext(ctx, "sentry", nil)
		sessionID := fmt.Sprintf("sentry-%d", time.Now().Unix())
		var err error
		var auditEventID string
		sentryResults, auditEventID, err = h.RunCognitiveLoop(ctx, h, "sentry", h.Config.Models["sentry"], sessionID, "", input, 1)
		lastAuditID = auditEventID

		if err == nil {
			utils.RecordProcessOutcome(ctx, h.RedisClient, h.Config.ProcessID, "success")
			for i := range sentryResults {
				sentryResults[i].Type = "alert"
				_, _ = h.emitResult(ctx, sentryResults[i], "sentry", auditEventID)
			}
			h.RedisClient.Set(ctx, "guardian:last_run:sentry", time.Now().Unix(), 0)
		} else {
			utils.RecordProcessOutcome(ctx, h.RedisClient, h.Config.ProcessID, "error")
		}
	}

	return sentryResults, lastAuditID, nil
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

	return corrections
}

func (h *GuardianHandler) gatherContext(ctx context.Context, tier string, previousResults []agent.AnalysisResult) string {
	status, _ := h.fetchSystemStatus(ctx)
	logs, _ := h.fetchRecentLogs(ctx)

	var cliHelp, tests, events, systemInfo, knownIssues string
	switch tier {
	case "sentry":
		tests, _ = h.fetchTestResults(ctx)
		events, _ = h.fetchEventsForAnalysis(ctx)
		systemInfo, _ = h.fetchSystemHardware(ctx)
		knownIssues = h.fetchRecentAlerts(ctx)
	}

	return h.formatContext(tier, status, logs, cliHelp, tests, events, systemInfo, knownIssues, previousResults)
}

func (h *GuardianHandler) formatContext(tier, status, logs, cliHelp, tests, events, systemInfo, knownIssues string, previousResults []agent.AnalysisResult) string {
	// Base context with common components (these all start and end with \n)
	context := fmt.Sprintf("## SYSTEM STATUS\n%s\n## LOGS\n%s",
		status, logs)

	switch tier {
	case "sentry":
		context += fmt.Sprintf("\n## TEST\n%s\n## SYSTEM\n%s\n## KNOWN ISSUES (Do not re-report)\n%s\n## EVENTS\n%s",
			tests, systemInfo, knownIssues, events)
	}

	return context
}

func (h *GuardianHandler) fetchRecentAlerts(ctx context.Context) string {
	ids, err := h.RedisClient.ZRevRange(ctx, "events:type:system.notification.generated", 0, 9).Result()
	if err != nil || len(ids) == 0 {
		return "None."
	}

	var alerts []string
	for _, id := range ids {
		val, err := h.RedisClient.Get(ctx, "event:"+id).Result()
		if err == nil {
			var e types.Event
			if json.Unmarshal([]byte(val), &e) == nil {
				var ed map[string]interface{}
				if json.Unmarshal(e.Event, &ed) == nil {
					// Check if it was an alert
					if isAlert, ok := ed["alert"].(bool); ok && isAlert {
						title, _ := ed["title"].(string)
						summary, _ := ed["summary"].(string)
						alerts = append(alerts, fmt.Sprintf("- **%s**: %s", title, summary))
					}
				}
			}
		}
	}
	return strings.Join(alerts, "\n")
}

// ... Context fetching methods remain largely similar but return strings for cleaner injection ...

func (h *GuardianHandler) emitResult(ctx context.Context, res agent.AnalysisResult, tier string, auditEventID string) (string, error) {
	eventID := uuid.New().String()
	timestamp := time.Now().Unix()
	payload := map[string]interface{}{
		"title": res.Title, "priority": res.Priority, "category": res.Category,
		"protocol": tier, "summary": res.Summary, "content": res.Content,
		"body": res.Body, "related_services": res.RelatedServices,
		"related_event_ids": res.RelatedEventIDs,
		"source_event_ids":  res.SourceEventIDs, "read": false,
		"audit_event_id": auditEventID,
	}

	eventType := string(types.EventTypeSystemNotificationGenerated)
	if res.Type == "alert" || res.Priority == "high" || res.Priority == "critical" {
		payload["alert"] = true
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
	// Iterate backwards to get oldest first
	for i := len(eventIDs) - 1; i >= 0; i-- {
		id := eventIDs[i]
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
	cmd := h.createDexCommand(ctx, "status", "--no-event")
	out, _ := cmd.CombinedOutput()
	return utils.StripANSI(string(out)), nil
}

func (h *GuardianHandler) fetchSystemHardware(ctx context.Context) (string, error) {
	cmd := h.createDexCommand(ctx, "system", "--no-event")
	out, _ := cmd.CombinedOutput()
	return utils.StripANSI(string(out)), nil
}

func (h *GuardianHandler) fetchRecentLogs(ctx context.Context) (string, error) {
	cmd := h.createDexCommand(ctx, "logs", "--no-event")
	out, _ := cmd.CombinedOutput()
	return utils.StripANSI(string(out)), nil
}

func (h *GuardianHandler) fetchTestResults(ctx context.Context) (string, error) {
	cmd := h.createDexCommand(ctx, "test", "--no-event")
	out, _ := cmd.CombinedOutput()
	return utils.StripANSI(string(out)), nil
}

func (h *GuardianHandler) createDexCommand(ctx context.Context, args ...string) *exec.Cmd {

	dexPath := h.getDexBinaryPath()

	fullCmd := fmt.Sprintf("%s %s", dexPath, strings.Join(args, " "))

	return exec.CommandContext(ctx, "bash", "-l", "-c", fullCmd)

}
