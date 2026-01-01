package profiler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/EasterCompany/dex-event-service/internal/agent"
	"github.com/EasterCompany/dex-event-service/internal/discord"
	"github.com/EasterCompany/dex-event-service/internal/ollama"
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
			EnforceJSON:     true,
			JSONSchema:      UserProfile{},
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

	// Enforce global sequential execution
	utils.AcquireCognitiveLock(ctx, h.RedisClient, h.Config.Name)
	defer utils.ReleaseCognitiveLock(ctx, h.RedisClient, h.Config.Name)

	utils.ReportProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID, fmt.Sprintf("Analyzing User: %s", targetUserID))
	defer utils.ClearProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID)

	// Set active tier for frontend visibility
	h.RedisClient.Set(ctx, "analyzer:active_tier", "synthesis", 0)
	defer h.RedisClient.Set(ctx, "analyzer:active_tier", "", 0)

	p, err := LoadProfile(ctx, h.RedisClient, targetUserID)
	if err != nil {
		return
	}

	input := h.gatherHistoryContext(ctx, targetUserID)
	sessionID := fmt.Sprintf("synthesis-%s-%d", targetUserID, time.Now().Unix())

	// Use RunCognitiveLoop which handles EnforceJSON and retries automatically
	results, auditID, err := h.RunCognitiveLoop(ctx, h, "synthesis", h.Config.Models["synthesis"], sessionID, h.buildSynthesisPrompt(p), input, 1)

	if err != nil {
		log.Printf("[%s] Synthesis loop failed: %v", h.Config.Name, err)
		// Backoff cooldown on cognitive failure
		h.RedisClient.Set(ctx, cooldownKey, "1", 30*time.Minute)
		return
	}

	if len(results) == 0 {
		// This happens if the model thinks no update is needed (e.g. stop token)
		log.Printf("[%s] No update needed for user %s", h.Config.Name, targetUserID)
		h.RedisClient.Set(ctx, cooldownKey, "1", 4*time.Hour) // Shorter cooldown for no-op
		return
	}

	// 3. Parse and Update (The loop already validated the JSON syntax)
	history, _ := h.ChatManager.LoadHistory(ctx, sessionID)
	if len(history) == 0 {
		return
	}
	resp := history[len(history)-1].Content

	var update UserProfile
	cleanJSON := h.CleanJSON(resp)
	if err := json.Unmarshal([]byte(cleanJSON), &update); err != nil {
		log.Printf("[%s] CRITICAL: JSON validation passed but Unmarshal failed: %v", h.Config.Name, err)
		return
	}

	// Preserve ID and core stats
	update.UserID = p.UserID
	update.Stats = p.Stats
	update.Identity.FirstSeen = p.Identity.FirstSeen

	if err := saveProfile(ctx, h.RedisClient, &update); err != nil {
		log.Printf("[%s] Failed to save updated profile: %v", h.Config.Name, err)
		return
	}

	// Update last run time
	h.RedisClient.Set(ctx, "analyzer:last_run:synthesis", time.Now().Unix(), 0)

	// Set long cooldown on success
	h.RedisClient.Set(ctx, cooldownKey, "1", time.Duration(h.Config.Cooldowns["synthesis"])*time.Second)

	log.Printf("[%s] Synthesis completed for user %s (Audit: %s)", h.Config.Name, targetUserID, auditID)
}

func (h *AnalyzerAgent) gatherHistoryContext(ctx context.Context, userID string) string {
	// TODO: Fetch last 50 analysis.user.message_signals for this user
	return "[Analytical signals for this period are being loaded...]"
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
