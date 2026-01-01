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
				"synthesis": "dex-master-model",
			},
			ProtocolAliases: map[string]string{
				"synthesis": "Synthesis",
			},
			Cooldowns: map[string]int{
				"synthesis": 43200, // 12 hours
			},
			IdleRequirement: 300, // 5 minutes idle (matched with Guardian)
			DateTimeAware:   true,
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

	// 1. Target Selection (For prototype, we'll pick the Master User, but we could scan keys)

	targetUserID := "313071000877137920"

	// Check cooldown

	cooldownKey := fmt.Sprintf("agent:%s:cooldown:%s", h.Config.Name, targetUserID)

	if h.RedisClient.Get(ctx, cooldownKey).Val() != "" {

		return

	}

	log.Printf("[%s] Starting synthesis for user %s", h.Config.Name, targetUserID)

	utils.ReportProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID, fmt.Sprintf("Analyzing User: %s", targetUserID))

	defer utils.ClearProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID)

	// Set active tier for frontend visibility

	h.RedisClient.Set(ctx, "analyzer:active_tier", "synthesis", 0)

	defer h.RedisClient.Set(ctx, "analyzer:active_tier", "", 0)

	p, err := LoadProfile(ctx, h.RedisClient, targetUserID)

	if err != nil {

		return

	}

	prompt := h.buildSynthesisPrompt(p)

	startTime := time.Now()

	model := h.Config.Models["synthesis"]

	var resp string

	var stats ollama.GenerationStats

	var genErr error

	var success bool

	var failReason string

	// Track attempts

	h.RedisClient.Incr(ctx, "system:metrics:model:"+model+":attempts")

	// Ensure Audit is ALWAYS emitted

	defer func() {

		if !success {

			h.RedisClient.Incr(ctx, "system:metrics:model:"+model+":failures")

			if strings.Contains(failReason, "JSON Parse Error") {

				h.RedisClient.Incr(ctx, "system:metrics:model:"+model+":absolute_failures")

			}

		}

		duration := time.Since(startTime)

		statusText := "SUCCESS"

		if !success {

			statusText = "FAILED: " + failReason

		}

		chatHistory := []map[string]string{

			{"role": "user", "content": prompt},

			{"role": "assistant", "content": resp},
		}

		auditEvent := map[string]interface{}{

			"type": types.EventTypeSystemAnalysisAudit,

			"source": "dex-event-service",

			"tier": "Synthesis",

			"agent_name": h.Config.Name,

			"success": success,

			"status": statusText,

			"duration": duration.String(),

			"model": h.Config.Models["synthesis"],

			"raw_input": prompt,

			"raw_output": resp,

			"chat_history": chatHistory,

			"eval_count": stats.EvalCount,

			"timestamp": time.Now().Unix(),

			"error": failReason,

			"attempts": h.RedisClient.Get(ctx, "system:metrics:model:"+model+":attempts").Val(),
		}

		_, _ = utils.SendEvent(ctx, h.RedisClient, "dex-event-service", string(types.EventTypeSystemAnalysisAudit), auditEvent)

	}()

	resp, stats, genErr = h.OllamaClient.Generate(h.Config.Models["synthesis"], prompt, nil)

	if genErr != nil {

		failReason = genErr.Error()

		// Short cooldown on connection failure

		h.RedisClient.Set(ctx, cooldownKey, "1", 5*time.Minute)

		return

	}

	// 3. Parse and Update

	var update UserProfile

	cleanJSON := h.CleanJSON(resp)

	if err := json.Unmarshal([]byte(cleanJSON), &update); err != nil {

		failReason = fmt.Sprintf("JSON Parse Error: %v", err)

		log.Printf("[%s] Failed to parse synthesis result: %v", h.Config.Name, err)

		// Backoff cooldown on model hallucination

		h.RedisClient.Set(ctx, cooldownKey, "1", 30*time.Minute)

		return

	}

	// Preserve ID and core stats

	update.UserID = p.UserID

	update.Stats = p.Stats

	update.Identity.FirstSeen = p.Identity.FirstSeen

	if err := saveProfile(ctx, h.RedisClient, &update); err != nil {

		failReason = "Save Error: " + err.Error()

		return

	}

	// Update last run time

	h.RedisClient.Set(ctx, "analyzer:last_run:synthesis", time.Now().Unix(), 0)

	// Set long cooldown on success

	h.RedisClient.Set(ctx, cooldownKey, "1", time.Duration(h.Config.Cooldowns["synthesis"])*time.Second)

	success = true

	log.Printf("[%s] Synthesis completed for user %s", h.Config.Name, targetUserID)

}

func (h *AnalyzerAgent) buildSynthesisPrompt(p *UserProfile) string {

	profileJSON, _ := json.MarshalIndent(p, "", "  ")

	return fmt.Sprintf(`You are the Analyzer, an advanced biographical synthesis engine.

Your task is to refine and update a User's Dossier based on their historical behavior.



### CURRENT PROFILE DATA (JSON):

%s



### INSTRUCTIONS:

1. Refine the profile. Be granular and clinical.

2. Logic check: Owen is the Creator/Master. His tech level is 11.

3. CRITICAL: Follow the exact JSON schema provided. 

   - "attributes" MUST be an ARRAY of objects: [{"key": "string", "value": "string", "confidence": float}]

   - "dossier.social" MUST be an ARRAY of objects: [{"name": "string", "relation": "string", "trust": "string"}]

   - Do NOT use objects/maps where arrays are expected.



Output the ENTIRE updated UserProfile object in valid JSON format.

Do not output any text other than the JSON.`, string(profileJSON))

}

func (h *AnalyzerAgent) CleanJSON(s string) string {
	if strings.Contains(s, "```json") {
		parts := strings.Split(s, "```json")
		if len(parts) > 1 {
			return strings.Split(parts[1], "```")[0]
		}
	}
	if strings.Contains(s, "```") {
		parts := strings.Split(s, "```")
		if len(parts) > 1 {
			return parts[1]
		}
	}
	return s
}
