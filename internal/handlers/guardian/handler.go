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
	"github.com/EasterCompany/dex-event-service/internal/model"
	"github.com/EasterCompany/dex-event-service/internal/web"
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

func NewGuardianHandler(redis *redis.Client, modelClient *model.Client, discord *discord.Client, web *web.Client, options interface{}) *GuardianHandler {
	ctx, cancel := context.WithCancel(context.Background())
	return &GuardianHandler{
		BaseAgent: agent.BaseAgent{
			RedisClient: redis,
			ModelClient: modelClient,
			ChatManager: utils.NewChatContextManager(redis),
			StopTokens:  []string{"<NO_ALERT/>", "<NO_BLUEPRINT/>", "<NO_ISSUES/>"},
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
				"SYSTEM", "ANOMALIES", "MAJOR ISSUES:", "MINOR ISSUES:",
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

			// Fetch the raw output from the audit event to get the full report
			val, _ := h.RedisClient.Get(ctx, "event:"+auditEventID).Result()
			var e types.Event
			var auditData struct {
				RawOutput string `json:"raw_output"`
			}
			if json.Unmarshal([]byte(val), &e) == nil {
				_ = json.Unmarshal(e.Event, &auditData)
			}

			report := auditData.RawOutput
			if report == "" && len(sentryResults) > 0 {
				report = sentryResults[0].Content
			}

			// Save to /tmp
			tmpPath := filepath.Join(os.TempDir(), fmt.Sprintf("guardian-sentry-report-%d.md", time.Now().Unix()))
			_ = os.WriteFile(tmpPath, []byte(report), 0644)
			log.Printf("[%s] Sentry report saved to %s", HandlerName, tmpPath)

			// Emit main event (24h TTL)
			res := agent.AnalysisResult{
				Title:   "Guardian Sentry Report",
				Summary: "System health check completed.",
				Content: report,
				Type:    "alert",
			}
			_, _ = h.emitResult(ctx, res, "sentry", auditEventID)

			// Check for MAJOR ISSUES and emit additional alert if found
			if strings.Contains(strings.ToUpper(report), "# MAJOR ISSUES:") && !strings.Contains(strings.ToUpper(report), "NONE.") {
				// Extract major issues for the alert summary
				majorIssuesSection := ""
				parts := strings.Split(report, "# MAJOR ISSUES:")
				if len(parts) > 1 {
					majorIssuesSection = strings.Split(parts[1], "#")[0]
				}

				if strings.TrimSpace(majorIssuesSection) != "" && !strings.Contains(strings.ToUpper(majorIssuesSection), "NONE.") {
					alertRes := agent.AnalysisResult{
						Title:    "MAJOR SYSTEM ISSUES DETECTED",
						Priority: "high",
						Category: "system",
						Summary:  "Guardian detected major system issues during Sentry analysis.",
						Content:  majorIssuesSection,
						Type:     "alert",
					}
					_, _ = h.emitResult(ctx, alertRes, "sentry", auditEventID)
				}
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

	var tests, events, systemInfo, knownIssues, sourceStatus string
	switch tier {
	case "sentry":
		tests, _ = h.fetchTestResults(ctx)
		events, _ = h.fetchEventsForAnalysis(ctx)
		systemInfo, _ = h.fetchSystemHardware(ctx)
		knownIssues = h.fetchRecentAlerts(ctx)
		sourceStatus, _ = h.fetchSourceCodeStatus(ctx)
	}

	return h.formatContext(tier, status, logs, tests, events, systemInfo, knownIssues, sourceStatus, previousResults)
}

func (h *GuardianHandler) formatContext(tier, status, logs, tests, events, systemInfo, knownIssues, sourceStatus string, previousResults []agent.AnalysisResult) string {
	// Base context with common components
	context := fmt.Sprintf("## SYSTEM STATUS (dex status)\n%s\n\n## LOGS (dex logs)\n%s",
		status, logs)

	switch tier {
	case "sentry":
		context += fmt.Sprintf("\n\n## SOURCE CODE STATUS (git status)\n%s\n\n## TEST RESULTS (dex test)\n%s\n\n## HARDWARE STATUS (dex system)\n%s\n\n## KNOWN ISSUES (Do not re-report)\n%s\n\n## EVENT TIMELINE (dex event log)\n%s",
			sourceStatus, tests, systemInfo, knownIssues, events)
	}

	return context
}

func (h *GuardianHandler) fetchSourceCodeStatus(ctx context.Context) (string, error) {
	home, _ := os.UserHomeDir()
	repoPath := filepath.Join(home, "EasterCompany")
	cmd := exec.CommandContext(ctx, "bash", "-l", "-c", fmt.Sprintf("cd %s && git status --short", repoPath))
	out, _ := cmd.CombinedOutput()
	return string(out), nil
}

func (h *GuardianHandler) fetchEventsForAnalysis(ctx context.Context) (string, error) {
	cmd := h.createDexCommand(ctx, "event", "log", "-n", "50")
	out, _ := cmd.CombinedOutput()
	return utils.StripANSI(string(out)), nil
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

func (h *GuardianHandler) createDexCommand(ctx context.Context, args ...string) *exec.Cmd {

	dexPath := h.getDexBinaryPath()

	fullCmd := fmt.Sprintf("%s %s", dexPath, strings.Join(args, " "))

	return exec.CommandContext(ctx, "bash", "-l", "-c", fullCmd)

}
