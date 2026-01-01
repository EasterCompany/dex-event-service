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
			IdleRequirement: 600, // 10 minutes idle
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
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-h.ctx.Done():
			return
		case <-ticker.C:
			// 1. Check Idle
			lastCognitiveEvent, _ := h.RedisClient.Get(h.ctx, "system:last_cognitive_event").Int64()
			now := time.Now().Unix()
			if now-lastCognitiveEvent < int64(h.Config.IdleRequirement) {
				continue
			}

			// 2. Perform Synthesis
			h.PerformSynthesis(h.ctx)
		}
	}
}

func (h *AnalyzerAgent) PerformSynthesis(ctx context.Context) {
	// Find users who need analysis
	// For this prototype, we'll fetch Owen (Master User) specifically to test,
	// then we could iterate over all user profiles in Redis.
	masterUserID := "313071000877137920"

	// Check cooldown
	cooldownKey := fmt.Sprintf("agent:%s:cooldown:%s", h.Config.Name, masterUserID)
	if h.RedisClient.Get(ctx, cooldownKey).Val() != "" {
		return
	}

	log.Printf("[%s] Starting synthesis for user %s", h.Config.Name, masterUserID)

	// Enforce global sequential execution
	utils.AcquireCognitiveLock(ctx, h.RedisClient, h.Config.Name)
	defer utils.ReleaseCognitiveLock(ctx, h.RedisClient, h.Config.Name)

	utils.ReportProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID, fmt.Sprintf("Analyzing User: %s", masterUserID))
	defer utils.ClearProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID)

	// Set active tier for frontend visibility
	h.RedisClient.Set(ctx, "analyzer:active_tier", "synthesis", 0)
	defer h.RedisClient.Set(ctx, "analyzer:active_tier", "", 0)

	// 1. Fetch Signal History
	// We'll search for 'analysis.user.message_signals' events for this user
	// This is slightly complex with current Redis schema (need to scan timeline or type list)
	// For now, let's assume we fetch the last 50 events from 'events:timeline' and filter.

	// Better: In a real system we'd have 'events:user:<id>'
	// Let's implement that in handleUserSignals!
	// But for now, we'll fetch from global.

	p, err := LoadProfile(ctx, h.RedisClient, masterUserID)
	if err != nil {
		return
	}

	prompt := h.buildSynthesisPrompt(p)

	startTime := time.Now()
	resp, stats, err := h.OllamaClient.Generate(h.Config.Models["synthesis"], prompt, nil)
	duration := time.Since(startTime)

	if err != nil {
		log.Printf("[%s] Synthesis failed: %v", h.Config.Name, err)
		return
	}

	// 3. Parse and Update
	var update UserProfile
	cleanJSON := h.CleanJSON(resp)
	if err := json.Unmarshal([]byte(cleanJSON), &update); err != nil {
		log.Printf("[%s] Failed to parse synthesis result: %v", h.Config.Name, err)
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

	// 4. Emit Audit Event
	chatHistory := []map[string]string{
		{"role": "user", "content": prompt},
		{"role": "assistant", "content": resp},
	}

	auditEvent := map[string]interface{}{
		"type":         types.EventTypeSystemAnalysisAudit,
		"source":       "dex-event-service",
		"tier":         "Synthesis",
		"agent_name":   h.Config.Name,
		"success":      true,
		"duration":     duration.String(),
		"model":        h.Config.Models["synthesis"],
		"raw_input":    prompt,
		"raw_output":   resp,
		"chat_history": chatHistory,
		"eval_count":   stats.EvalCount,
		"timestamp":    time.Now().Unix(),
	}
	_, _ = utils.SendEvent(ctx, h.RedisClient, "dex-event-service", string(types.EventTypeSystemAnalysisAudit), auditEvent)

	// 5. Set Cooldown
	h.RedisClient.Set(ctx, cooldownKey, "1", time.Duration(h.Config.Cooldowns["synthesis"])*time.Second)
	log.Printf("[%s] Synthesis completed for user %s", h.Config.Name, masterUserID)
}
func (h *AnalyzerAgent) buildSynthesisPrompt(p *UserProfile) string {
	// In a real system, we'd include message history here.
	// For the first version, we'll ask it to refine based on existing facts.
	profileJSON, _ := json.MarshalIndent(p, "", "  ")

	return fmt.Sprintf(`You are the Analyzer, an advanced biographical synthesis engine.
Your task is to refine and update a User's Dossier based on their historical behavior and extracted signals.

Current Profile Data:
%s

Refine the profile. Be granular and clinical.
Update fields like 'dossier' (Career, Sexuality, Social, Personal) based on logical inferences from their behavior (Master of Go, System Architect, Creator of Dexter).

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
