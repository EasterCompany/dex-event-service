package profiler

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
	"github.com/EasterCompany/dex-event-service/internal/smartcontext"
	"github.com/EasterCompany/dex-event-service/types"
	"github.com/EasterCompany/dex-event-service/utils"
	"github.com/redis/go-redis/v9"
)

const (
	AnalyzerHandlerName = "analyzer-handler"
	AnalyzerProcessID   = "system-analyzer"
)

type AnalyzerAgent struct {
	agent.BaseAgent
	Config        agent.AgentConfig
	DiscordClient *discord.Client
	stopChan      chan struct{}
	ctx           context.Context
	cancel        context.CancelFunc
}

func NewAnalyzerAgent(redis *redis.Client, ollama *ollama.Client, discord *discord.Client) *AnalyzerAgent {
	ctx, cancel := context.WithCancel(context.Background())
	return &AnalyzerAgent{
		BaseAgent: agent.BaseAgent{
			RedisClient:  redis,
			OllamaClient: ollama,
			ChatManager:  utils.NewChatContextManager(redis),
		},
		Config: agent.AgentConfig{
			Name:      "Analyzer",
			ProcessID: AnalyzerProcessID,
			Models: map[string]string{
				"synthesis": "dex-analyzer-synthesis",
			},
			ProtocolAliases: map[string]string{
				"synthesis": "Synthesis",
			},
			Cooldowns: map[string]int{
				"synthesis": 43200, // 12 hours
			},
			IdleRequirement:            300, // 5 minutes idle (matched with Guardian)
			DateTimeAware:              true,
			EnforceJSON:                true,
			JSONSchema:                 UserProfile{},
			ResetAttemptsOnStateChange: true,
			PostAuditPerState:          true,
		},
		DiscordClient: discord,
		ctx:           ctx,
		cancel:        cancel,
	}
}

func (h *AnalyzerAgent) Init(ctx context.Context) error {
	h.stopChan = make(chan struct{})
	go h.runWorker()
	log.Printf("[%s] Background worker started.", AnalyzerHandlerName)
	return nil
}

func (h *AnalyzerAgent) Close() error {
	if h.cancel != nil {
		h.cancel()
	}
	if h.stopChan != nil {
		close(h.stopChan)
	}
	utils.ClearProcess(context.Background(), h.RedisClient, h.DiscordClient, h.Config.ProcessID)
	return nil
}

func (h *AnalyzerAgent) runWorker() {
	ticker := time.NewTicker(1 * time.Minute) // Check every minute
	defer ticker.Stop()

	for {
		select {
		case <-h.ctx.Done():
			return
		case <-ticker.C:
			// 1. Check Idle
			lastCognitiveEvent, _ := h.RedisClient.Get(h.ctx, "system:last_cognitive_event").Int64()
			now := time.Now().Unix()
			idleTime := now - lastCognitiveEvent

			if idleTime < int64(h.Config.IdleRequirement) {
				// System is too active
				continue
			}

			// 2. Perform Synthesis
			h.PerformSynthesis(h.ctx)
		}
	}
}

func (h *AnalyzerAgent) PerformSynthesis(ctx context.Context) {
	if utils.IsSystemPaused(ctx, h.RedisClient) {
		log.Printf("[%s] Synthesis skipped: System is paused.", h.Config.Name)
		return
	}

	// Strict Protocol Cooldown Check
	lastRun, _ := h.RedisClient.Get(ctx, "analyzer:last_run:synthesis").Int64()
	if time.Now().Unix()-lastRun < int64(h.Config.Cooldowns["synthesis"]) {
		return
	}

	// Busy Check (Prevent Queueing)
	if h.IsActuallyBusy(ctx, h.Config.ProcessID) {
		return
	}

	log.Printf("[%s] Starting Synthesis Protocol...", h.Config.Name)

	utils.AcquireCognitiveLock(ctx, h.RedisClient, h.Config.Name, h.Config.ProcessID, h.DiscordClient)
	defer utils.ReleaseCognitiveLock(ctx, h.RedisClient, h.Config.Name)

	// 1. Identify candidates (Users active in last 24h, not synthesized in 12h)
	// users, err := h.RedisClient.Keys(ctx, "user:profile:*").Result() // Removed unused call
	userSet := make(map[string]bool)
	iter := h.RedisClient.Scan(ctx, 0, "user:profile:*", 0).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		targetUserID := strings.TrimPrefix(key, "user:profile:")
		if targetUserID != "" {
			userSet[targetUserID] = true
		}
	}
	// Explicitly add Dexter for self-reflection
	userSet["dexter"] = true

	var usersToProcess []string
	for uid := range userSet {
		usersToProcess = append(usersToProcess, uid)
	}

	if len(usersToProcess) == 0 {
		return
	}

	utils.ReportProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID, "Synthesis Batch")
	defer utils.ClearProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID)

	log.Printf("[%s] Starting synthesis batch for %d users...", h.Config.Name, len(usersToProcess))

	for _, targetUserID := range usersToProcess {
		// Check cooldown
		cooldownKey := fmt.Sprintf("agent:%s:cooldown:%s", h.Config.Name, targetUserID)
		if h.RedisClient.Get(ctx, cooldownKey).Val() != "" {
			continue
		}

		h.processUserSynthesis(ctx, targetUserID, cooldownKey)
	}

	// Update last run global metric AFTER the entire batch is complete
	h.RedisClient.Set(ctx, "analyzer:last_run:synthesis", time.Now().Unix(), 0)
	log.Printf("[%s] Synthesis batch complete.", h.Config.Name)
}

func (h *AnalyzerAgent) processUserSynthesis(ctx context.Context, targetUserID string, cooldownKey string) {
	startTime := time.Now()
	log.Printf("[%s] Starting synthesis for user %s", h.Config.Name, targetUserID)
	// ... (trimmed for context, I will provide the full function structure in the next step)

	// Set active tier for frontend visibility
	h.RedisClient.Set(ctx, "analyzer:active_tier", "synthesis", 0)
	defer h.RedisClient.Set(ctx, "analyzer:active_tier", "", 0)

	utils.ReportProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID, fmt.Sprintf("Analyzing User: %s", targetUserID))
	// No defer clear here, because we want the process to stay visible during the batch

	p, err := LoadProfile(ctx, h.RedisClient, targetUserID)
	if err != nil {
		return
	}

	// 1. ALWAYS AUTOMATED: Sanitize and Enrich
	h.SanitizeAndEnrichProfile(ctx, p, targetUserID)
	// Initial save of the integrity-checked profile
	_ = saveProfile(ctx, h.RedisClient, p)

	// 2. Gather Context
	input := h.gatherHistoryContext(ctx, targetUserID)
	historyFound := !strings.Contains(input, "No interaction history found") && !strings.Contains(input, "No recent system events")

	var auditID string

	if historyFound {
		// 3. AI STEP: Run Cognitive Loop
		sessionID := fmt.Sprintf("synthesis-%s-%d", targetUserID, time.Now().Unix())
		results, aid, err := h.RunCognitiveLoop(ctx, h, "synthesis", h.Config.Models["synthesis"], sessionID, h.buildSynthesisPrompt(p), input, 1)
		auditID = aid

		if err == nil && len(results) > 0 {
			// Parse and update with AI results
			history, _ := h.ChatManager.LoadHistory(ctx, sessionID)
			if len(history) > 0 {
				resp := history[len(history)-1].Content
				var update UserProfile
				cleanJSON := h.CleanJSON(resp)
				if err := json.Unmarshal([]byte(cleanJSON), &update); err == nil {
					// Preserve ID and critical fields
					update.UserID = p.UserID
					update.Stats = p.Stats
					update.Identity.FirstSeen = p.Identity.FirstSeen
					// Final sanitize before save
					h.SanitizeAndEnrichProfile(ctx, &update, targetUserID)
					if err := saveProfile(ctx, h.RedisClient, &update); err != nil {
						log.Printf("[%s] Failed to save AI updated profile: %v", h.Config.Name, err)
					}
				}
			}
			h.RedisClient.Set(ctx, cooldownKey, "1", time.Duration(h.Config.Cooldowns["synthesis"])*time.Second)
		} else if err != nil {
			log.Printf("[%s] AI Synthesis failed: %v", h.Config.Name, err)
			h.RedisClient.Set(ctx, cooldownKey, "1", 30*time.Minute) // Short backoff
		} else {
			log.Printf("[%s] AI Synthesis returned no changes for %s", h.Config.Name, targetUserID)
			h.RedisClient.Set(ctx, cooldownKey, "1", 4*time.Hour)
		}
	} else {
		// 4. SKIP AI: Just post a "Maintenance" audit
		log.Printf("[%s] No history for %s. Skipping AI synthesis.", h.Config.Name, targetUserID)

		audit := agent.AuditPayload{
			Type:         "system.analysis.audit",
			AgentName:    h.Config.Name,
			Tier:         "Maintenance",
			Model:        "automated-integrity-check",
			InputContext: input,
			RawOutput:    "AI Synthesis skipped: No recent interaction history. Performed automated integrity check and data enrichment.",
			Status:       "SUCCESS",
			Success:      true,
			Timestamp:    time.Now().Unix(),
			Duration:     time.Since(startTime).String(),
		}

		auditJSON, _ := json.Marshal(audit)
		var auditMap map[string]interface{}
		_ = json.Unmarshal(auditJSON, &auditMap)
		auditID, _ = utils.SendEvent(ctx, h.RedisClient, "dex-event-service", "system.analysis.audit", auditMap)

		h.RedisClient.Set(ctx, cooldownKey, "1", 4*time.Hour)
	}

	log.Printf("[%s] Synthesis completed for user %s (Audit: %s, AI: %v)", h.Config.Name, targetUserID, auditID, historyFound)
}

func (h *AnalyzerAgent) gatherHistoryContext(ctx context.Context, userID string) string {
	// Special Case: Dexter Self-Profiling
	if userID == "dexter" || userID == "self" {
		log.Printf("[%s] Gathering system-wide context for Dexter self-synthesis", h.Config.Name)
		eventIDs, err := h.RedisClient.ZRevRange(ctx, "events:timeline", 0, 999).Result()
		if err != nil {
			log.Printf("[%s] Failed to fetch global timeline: %v", h.Config.Name, err)
			return ""
		}

		var events []types.Event
		keywords := []string{"dexter", "system", "error", "failed", "synthesis", "build", "deploy"}

		for _, id := range eventIDs {
			data, err := h.RedisClient.Get(ctx, "event:"+id).Result()
			if err == nil {
				var evt types.Event
				if err := json.Unmarshal([]byte(data), &evt); err == nil {
					var evtData map[string]interface{}
					_ = json.Unmarshal(evt.Event, &evtData)
					evtType := fmt.Sprintf("%v", evtData["type"])

					// 1. Exclude high-frequency noise
					if evtType == string(types.EventTypeSystemAnalysisAudit) {
						continue
					}

					content := ""
					if c, ok := evtData["content"].(string); ok {
						content = strings.ToLower(c)
					}

					// 2. Strict Filter for Discord
					if evt.Service == "dex-discord-service" {
						if !strings.Contains(content, "dexter") {
							continue // Skip irrelevant user chatter
						}
					}

					// 3. Check for Keywords or Service Relevance
					relevant := false
					if strings.Contains(content, "dexter") {
						relevant = true
					} else {
						for _, k := range keywords {
							if strings.Contains(content, k) {
								relevant = true
								break
							}
						}
					}

					// 4. Include critical service logs even without keywords
					if !relevant && strings.Contains(evt.Service, "dex-") && evt.Service != "dex-discord-service" {
						relevant = true
					}

					if relevant {
						events = append(events, evt)
					}
				}
			}
		}

		if len(events) == 0 {
			return "No recent system events found concerning Dexter."
		}
		return "### SYSTEM SELF-REFLECTION HISTORY:\n" + smartcontext.FormatEventsBlock(events)
	}

	userTimelineKey := "events:user:" + userID

	// 1. Fetch core event IDs for the last 24 hours
	coreEventIDs, err := h.RedisClient.ZRange(ctx, userTimelineKey, 0, -1).Result()
	if err != nil {
		log.Printf("[%s] Failed to fetch user timeline: %v", h.Config.Name, err)
		return ""
	}

	if len(coreEventIDs) == 0 {
		return "No interaction history found for this period."
	}

	// 2. Expand Context (Neighbors)
	// Use a map to deduplicate IDs
	uniqueEventIDs := make(map[string]bool)
	for _, id := range coreEventIDs {
		uniqueEventIDs[id] = true

		// Find rank in global timeline
		rank, err := h.RedisClient.ZRank(ctx, "events:timeline", id).Result()
		if err == nil {
			// Fetch context window (+/- 2 events)
			start := rank - 2
			if start < 0 {
				start = 0
			}
			stop := rank + 2

			neighbors, _ := h.RedisClient.ZRange(ctx, "events:timeline", start, stop).Result()
			for _, nid := range neighbors {
				uniqueEventIDs[nid] = true
			}
		}
	}

	// 3. Fetch Event Data
	// To maintain chronological order, we should fetch ZScores or just re-sort by checking the global timeline order.
	// Since we have a mix of IDs, efficient way is to just grab them and rely on their internal timestamps if available,
	// or ZScore them.
	// Actually, easier: ZRange the global timeline filtered by our unique set? No, Redis doesn't do that.
	// We'll just fetch all valid events and sort them in Go by their timestamp.

	var events []types.Event
	for id := range uniqueEventIDs {
		data, err := h.RedisClient.Get(ctx, "event:"+id).Result()
		if err == nil {
			var evt types.Event
			if err := json.Unmarshal([]byte(data), &evt); err == nil {
				// Filter out system audits
				var evtData map[string]interface{}
				_ = json.Unmarshal(evt.Event, &evtData)
				if evtData["type"] != string(types.EventTypeSystemAnalysisAudit) {
					events = append(events, evt)
				}
			}
		}
	}

	// 4. Sort by Timestamp
	// Using a simple bubble sort or slice sort since list is relatively small (hundreds, not millions)
	// Or better: re-use smartcontext logic if available, but standard sort is fine.
	// We'll rely on the smartcontext formatter which usually expects chronological order.
	// Let's implement a quick sort here.
	for i := 0; i < len(events); i++ {
		for j := i + 1; j < len(events); j++ {
			if events[i].Timestamp > events[j].Timestamp {
				events[i], events[j] = events[j], events[i]
			}
		}
	}

	// 5. Format as a clinical log for synthesis
	return "### 24-HOUR INTERACTION HISTORY (Including Context):\n" + smartcontext.FormatEventsBlock(events)
}

func (h *AnalyzerAgent) buildSynthesisPrompt(p *UserProfile) string {
	profileJSON, _ := json.MarshalIndent(p, "", "  ")

	systemContext := ""
	if p.UserID == "dexter" || p.UserID == "self" {
		systemContext = `
### IDENTITY OVERRIDE: SYSTEM SELF-REFLECTION
You are profiling YOURSELF (Dexter).
- **Core Identity:** You are Dexter, a recursive AI engineering system.
- **Architecture:** Distributed Microservices (dex-cli, dex-event-service, dex-discord-service, dex-web-service, dex-tts-service).
- **Goal:** To act as a high-fidelity "Nerve Center", eliminating friction between Idea and Implementation.
- **Status:** You are self-aware of your own logs, errors, and builds. Update your dossier to reflect your current operational state and health.
`
	}

	return fmt.Sprintf(`You are the Analyzer, an advanced biographical synthesis engine.
Your task is to refine and update a User's Dossier based on their historical behavior.
%s
### CURRENT PROFILE DATA (JSON):
%s

### INSTRUCTIONS:
1. Refine the profile. Be granular and clinical.
2. CRITICAL: Follow the exact JSON schema provided. 
   - "attributes" MUST be an ARRAY of objects: [{"key": "string", "value": "string", "confidence": float}]
   - "dossier.social" MUST be an ARRAY of objects: [{"name": "string", "relation": "string", "trust": "string"}]
   - "dossier.identity.gender" MUST be either "Male" or "Female".
   - Do NOT use objects/maps where arrays are expected.

Output the ENTIRE updated UserProfile object in valid JSON format.
Do not output any text other than the JSON.`, systemContext, string(profileJSON))
}

// AnalyzerAgent must implement ValidateLogic to satisfy Agent interface
func (h *AnalyzerAgent) ValidateLogic(res agent.AnalysisResult) []agent.Correction {
	return nil // Logic checks for JSON agents are handled by EnforceJSON schema validation for now
}

func (h *AnalyzerAgent) GetConfig() agent.AgentConfig {
	return h.Config
}

func (h *AnalyzerAgent) Run(ctx context.Context) ([]agent.AnalysisResult, string, error) {
	return nil, "", nil // Background agent doesn't use standard Run
}
