package imaginator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/EasterCompany/dex-event-service/internal/agent"
	"github.com/EasterCompany/dex-event-service/internal/discord"
	"github.com/EasterCompany/dex-event-service/internal/ollama"
	"github.com/EasterCompany/dex-event-service/types"
	"github.com/EasterCompany/dex-event-service/utils"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	HandlerName         = "imaginator-handler"
	ImaginatorProcessID = "system-imaginator"
)

type ImaginatorHandler struct {
	agent.BaseAgent
	Config        agent.AgentConfig
	DiscordClient *discord.Client
	stopChan      chan struct{}
	ctx           context.Context
	cancel        context.CancelFunc
}

func NewImaginatorHandler(redis *redis.Client, ollama *ollama.Client, discord *discord.Client) *ImaginatorHandler {
	ctx, cancel := context.WithCancel(context.Background())
	return &ImaginatorHandler{
		BaseAgent: agent.BaseAgent{
			RedisClient:  redis,
			OllamaClient: ollama,
			ChatManager:  utils.NewChatContextManager(redis),
			StopTokens:   []string{"<NO_BLUEPRINT/>", "<IGNORE_ALERT/>"},
		},
		Config: agent.AgentConfig{
			Name:      "Imaginator",
			ProcessID: ImaginatorProcessID,
			Models: map[string]string{
				"alert_review": "dex-imaginator-model", // Use a specialized model if available, or guardian/master
			},
			ProtocolAliases: map[string]string{
				"alert_review": "AlertReview",
			},
			Cooldowns: map[string]int{
				"alert_review": 60, // Check frequently for new alerts
			},
			IdleRequirement: 0, // Reacts to alerts, doesn't need system idle
			DateTimeAware:   true,
			EnforceMarkdown: true,
			RequiredSections: []string{
				"Decision", "Reasoning", "Blueprint_Title", "Blueprint_Summary", "Implementation_Steps",
			},
		},
		DiscordClient: discord,
		ctx:           ctx,
		cancel:        cancel,
	}
}

func (h *ImaginatorHandler) Init(ctx context.Context) error {
	h.stopChan = make(chan struct{})
	go h.runWorker()
	log.Printf("[%s] Background worker started.", HandlerName)
	return nil
}

func (h *ImaginatorHandler) Close() error {
	if h.cancel != nil {
		h.cancel()
	}
	if h.stopChan != nil {
		close(h.stopChan)
	}
	utils.ClearProcess(context.Background(), h.RedisClient, h.DiscordClient, h.Config.ProcessID)
	return nil
}

func (h *ImaginatorHandler) GetConfig() agent.AgentConfig {
	return h.Config
}

func (h *ImaginatorHandler) Run(ctx context.Context) ([]agent.AnalysisResult, string, error) {
	// For Imaginator, Run is triggered via checkAndAnalyze -> PerformAnalysis
	// But to satisfy interface, we can just return empty here or trigger a single analysis if needed.
	// Since Imaginator is event-driven (alerts), a generic Run might just check for alerts.
	h.checkAndAnalyze()
	return nil, "", nil
}

func (h *ImaginatorHandler) runWorker() {
	ticker := time.NewTicker(1 * time.Minute)
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

func (h *ImaginatorHandler) checkAndAnalyze() {
	ctx := h.ctx

	// 1. Check for unprocessed alerts
	alerts, err := h.fetchUnprocessedAlerts(ctx)
	if err != nil || len(alerts) == 0 {
		return
	}

	// 2. No ongoing processes (True Busy Check)
	if h.IsActuallyBusy(ctx, h.Config.ProcessID) {
		return
	}

	// Trigger analysis for the oldest unprocessed alert
	_, _, err = h.PerformAnalysis(ctx, alerts[0])
	if err != nil {
		log.Printf("[%s] Analysis failed: %v", HandlerName, err)
	}
}

func (h *ImaginatorHandler) PerformAnalysis(ctx context.Context, alert types.Event) ([]agent.AnalysisResult, string, error) {
	log.Printf("[%s] Starting Imaginator Analysis on Alert: %s", HandlerName, alert.ID)

	// Enforce global sequential execution
	utils.AcquireCognitiveLock(ctx, h.RedisClient, h.Config.Name)
	defer utils.ReleaseCognitiveLock(ctx, h.RedisClient, h.Config.Name)

	utils.ReportProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID, "Alert Review Protocol")
	defer utils.ClearProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID)

	// Gather context from the alert
	input := h.formatAlertContext(alert)
	sessionID := fmt.Sprintf("imaginator-%s-%d", alert.ID, time.Now().Unix())

	results, auditEventID, err := h.RunCognitiveLoop(ctx, h, "alert_review", h.Config.Models["alert_review"], sessionID, "", input, 1)

	if err == nil {
		utils.RecordProcessOutcome(ctx, h.RedisClient, h.Config.ProcessID, "success")
		for _, res := range results {
			// Process decision
			if res.Title == "IGNORE" || strings.Contains(res.Summary, "IGNORE") {
				h.markAlertAsProcessed(ctx, alert.ID, "ignored")
				log.Printf("[%s] Alert %s ignored: %s", HandlerName, alert.ID, res.Summary)
			} else {
				// Valid blueprint
				res.Type = "blueprint"
				res.Body = res.Summary
				res.SourceEventIDs = []string{alert.ID}
				if _, err := h.emitBlueprint(ctx, res, "alert_review", auditEventID); err != nil {
					log.Printf("[%s] Failed to emit blueprint: %v", HandlerName, err)
				}
				h.markAlertAsProcessed(ctx, alert.ID, "processed")
			}
		}
	} else {
		utils.RecordProcessOutcome(ctx, h.RedisClient, h.Config.ProcessID, "error")
	}

	return results, auditEventID, nil
}

// ValidateLogic implements protocol-specific logical checks.
func (h *ImaginatorHandler) ValidateLogic(res agent.AnalysisResult) []agent.Correction {
	var corrections []agent.Correction

	// If decision is to act, ensure blueprint steps
	if res.Title != "IGNORE" && len(res.ImplementationPath) == 0 {
		corrections = append(corrections, agent.Correction{
			Type: "LOGIC", Guidance: "If this is a valid issue, you MUST provide a 'Proposed Steps' section with at least one technical step.", Mandatory: true,
		})
	}

	return corrections
}

func (h *ImaginatorHandler) fetchUnprocessedAlerts(ctx context.Context) ([]types.Event, error) {
	// Fetch alerts from the "events:type:system.notification.generated" pool
	// Filter for those NOT in "imaginator:processed_alerts" set
	// This is a simplification; optimal approach needs a proper queue or status field

	// For now, let's grab the last 10 alerts and check if processed
	ids, err := h.RedisClient.ZRevRange(ctx, "events:type:system.notification.generated", 0, 9).Result()
	if err != nil {
		return nil, err
	}

	var unprocessed []types.Event
	for _, id := range ids {
		isProcessed, _ := h.RedisClient.SIsMember(ctx, "imaginator:processed_alerts", id).Result()
		if !isProcessed {
			val, err := h.RedisClient.Get(ctx, "event:"+id).Result()
			if err == nil {
				var e types.Event
				if err := json.Unmarshal([]byte(val), &e); err == nil {
					// Check if it's an actual alert (priority high/critical usually)
					var ed map[string]interface{}
					if json.Unmarshal(e.Event, &ed) == nil {
						if p, ok := ed["priority"].(string); ok && (p == "high" || p == "critical") {
							unprocessed = append(unprocessed, e)
						}
					}
				}
			}
		}
	}
	return unprocessed, nil
}

func (h *ImaginatorHandler) markAlertAsProcessed(ctx context.Context, alertID, status string) {
	h.RedisClient.SAdd(ctx, "imaginator:processed_alerts", alertID)
	// Optional: Store status metadata if needed
}

func (h *ImaginatorHandler) formatAlertContext(alert types.Event) string {
	var ed map[string]interface{}
	if err := json.Unmarshal(alert.Event, &ed); err != nil {
		log.Printf("[%s] Failed to unmarshal alert event for context: %v", HandlerName, err)
		return "Error: Could not parse alert content."
	}

	context := "## ALERT TO REVIEW\n"
	context += fmt.Sprintf("**ID**: %s\n", alert.ID)
	context += fmt.Sprintf("**Timestamp**: %s\n", time.Unix(alert.Timestamp, 0).Format(time.RFC3339))
	context += fmt.Sprintf("**Title**: %v\n", ed["title"])
	context += fmt.Sprintf("**Body**: %v\n", ed["body"])
	context += fmt.Sprintf("**Category**: %v\n", ed["category"])

	return context
}

func (h *ImaginatorHandler) emitBlueprint(ctx context.Context, res agent.AnalysisResult, tier string, auditEventID string) (string, error) {
	eventID := uuid.New().String()
	timestamp := time.Now().Unix()

	payload := map[string]interface{}{
		"type":                string(types.EventTypeSystemBlueprintGenerated),
		"blueprint":           true,
		"title":               res.Title,
		"summary":             res.Summary,
		"content":             res.Content,
		"body":                res.Body,
		"implementation_path": res.ImplementationPath,
		"related_services":    res.RelatedServices,
		"source_event_ids":    res.SourceEventIDs,
		"protocol":            tier,
		"audit_event_id":      auditEventID,
		"category":            "fix", // Default category for now
		"priority":            "high",
		"read":                false,
	}

	eventJSON, _ := json.Marshal(payload)
	event := types.Event{ID: eventID, Service: HandlerName, Event: eventJSON, Timestamp: timestamp}
	fullJSON, _ := json.Marshal(event)

	pipe := h.RedisClient.Pipeline()
	pipe.Set(ctx, "event:"+eventID, fullJSON, utils.DefaultTTL)
	pipe.ZAdd(ctx, "events:timeline", redis.Z{Score: float64(timestamp), Member: eventID})
	pipe.ZAdd(ctx, "events:service:"+HandlerName, redis.Z{Score: float64(timestamp), Member: eventID})
	pipe.ZAdd(ctx, "events:type:system.blueprint.generated", redis.Z{Score: float64(timestamp), Member: eventID})

	_, err := pipe.Exec(ctx)
	return eventID, err
}
