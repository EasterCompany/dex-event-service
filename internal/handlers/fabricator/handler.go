package fabricator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
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
	HandlerName         = "fabricator-handler"
	FabricatorProcessID = "system-fabricator"
)

type FabricatorHandler struct {
	agent.BaseAgent
	Config        agent.AgentConfig
	DiscordClient *discord.Client
	stopChan      chan struct{}
	ctx           context.Context
	cancel        context.CancelFunc
}

func NewFabricatorHandler(redis *redis.Client, ollama *ollama.Client, discord *discord.Client) *FabricatorHandler {
	ctx, cancel := context.WithCancel(context.Background())
	return &FabricatorHandler{
		BaseAgent: agent.BaseAgent{
			RedisClient:  redis,
			OllamaClient: ollama,
			ChatManager:  utils.NewChatContextManager(redis),
		},
		Config: agent.AgentConfig{
			Name:      "Fabricator",
			ProcessID: FabricatorProcessID,
			Models: map[string]string{
				"construction": "fabricator-cli-yolo",
			},
			ProtocolAliases: map[string]string{
				"construction": "Construction",
			},
			Cooldowns: map[string]int{
				"construction": 86400,
			},
			IdleRequirement: 300,
		},
		DiscordClient: discord,
		ctx:           ctx,
		cancel:        cancel,
	}
}

func (h *FabricatorHandler) Init(ctx context.Context) error {
	h.stopChan = make(chan struct{})
	go h.runWorker()
	log.Printf("[%s] Background worker started.", HandlerName)
	return nil
}

func (h *FabricatorHandler) Close() error {
	if h.cancel != nil {
		h.cancel()
	}
	if h.stopChan != nil {
		close(h.stopChan)
	}
	utils.ClearProcess(context.Background(), h.RedisClient, h.DiscordClient, h.Config.ProcessID)
	return nil
}

func (h *FabricatorHandler) GetConfig() agent.AgentConfig {
	return h.Config
}

func (h *FabricatorHandler) Run(ctx context.Context) ([]agent.AnalysisResult, string, error) {
	h.checkAndFabricate()
	return nil, "", nil
}

func (h *FabricatorHandler) runWorker() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-h.stopChan:
			return
		case <-ticker.C:
			h.checkAndFabricate()
		}
	}
}

func (h *FabricatorHandler) checkAndFabricate() {
	ctx := h.ctx

	if utils.IsSystemPaused(ctx, h.RedisClient) {
		return
	}

	// 1. Fetch Approved Blueprints
	blueprint, err := h.fetchApprovedBlueprint(ctx)
	if err != nil || blueprint == nil {
		return
	}

	// 2. Check Locks
	if h.IsActuallyBusy(ctx, h.Config.ProcessID) {
		return
	}

	// 3. Perform Fabrication
	_, _, _ = h.PerformFabrication(ctx, *blueprint)
}

func (h *FabricatorHandler) PerformFabrication(ctx context.Context, blueprint types.Event) ([]agent.AnalysisResult, string, error) {
	log.Printf("[%s] Starting Fabricator Construction", HandlerName)

	// Enforce global sequential execution
	utils.AcquireCognitiveLock(ctx, h.RedisClient, h.Config.Name, h.Config.ProcessID, h.DiscordClient)
	defer utils.ReleaseCognitiveLock(ctx, h.RedisClient, h.Config.Name)

	utils.ReportProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID, "Construction Protocol")
	defer utils.ClearProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID)

	h.RedisClient.Set(ctx, "fabricator:active_tier", "construction", utils.DefaultTTL)
	defer h.RedisClient.Del(ctx, "fabricator:active_tier")

	// Gather Context
	prompt := h.buildPrompt(blueprint)

	// Execute Fabricator CLI
	binPath, err := exec.LookPath("fabricator")
	if err != nil {
		log.Printf("[%s] Error: 'fabricator' binary not found in PATH.", HandlerName)
		utils.RecordProcessOutcome(ctx, h.RedisClient, h.Config.ProcessID, "error")
		return nil, "", err
	}

	// Use --yolo for autonomous execution
	cmd := exec.CommandContext(ctx, binPath, "--yolo")
	cmd.Stdin = strings.NewReader(prompt)

	// Capture output
	out, err := cmd.CombinedOutput()
	output := string(out)

	if err != nil {
		log.Printf("[%s] Fabrication failed: %v\nOutput: %s", HandlerName, err, output)
		utils.RecordProcessOutcome(ctx, h.RedisClient, h.Config.ProcessID, "error")
		// Mark as processed to prevent infinite loop on same blueprint
		h.markBlueprintAsProcessed(ctx, blueprint.ID)
		return nil, "", err
	}

	log.Printf("[%s] Fabrication complete.\nOutput: %s", HandlerName, output)
	utils.RecordProcessOutcome(ctx, h.RedisClient, h.Config.ProcessID, "success")
	h.markBlueprintAsProcessed(ctx, blueprint.ID)
	h.RedisClient.Set(ctx, "fabricator:last_run:construction", time.Now().Unix(), 0)

	return nil, "", nil
}

func (h *FabricatorHandler) fetchApprovedBlueprint(ctx context.Context) (*types.Event, error) {
	// Look for 'system.blueprint.generated' events in timeline
	ids, err := h.RedisClient.ZRevRange(ctx, "events:type:system.blueprint.generated", 0, 19).Result()
	if err != nil {
		return nil, err
	}

	for _, id := range ids {
		// Check if processed
		isProcessed, _ := h.RedisClient.SIsMember(ctx, "fabricator:processed_blueprints", id).Result()
		if isProcessed {
			continue
		}

		// Fetch event
		val, err := h.RedisClient.Get(ctx, "event:"+id).Result()
		if err != nil {
			continue
		}

		var e types.Event
		if err := json.Unmarshal([]byte(val), &e); err != nil {
			continue
		}

		// Check if approved
		var ed map[string]interface{}
		if err := json.Unmarshal(e.Event, &ed); err != nil {
			continue
		}

		if approved, ok := ed["approved"].(bool); ok && approved {
			return &e, nil
		}
	}
	return nil, nil
}

func (h *FabricatorHandler) markBlueprintAsProcessed(ctx context.Context, id string) {
	h.RedisClient.SAdd(ctx, "fabricator:processed_blueprints", id)
}

func (h *FabricatorHandler) buildPrompt(blueprint types.Event) string {
	var ed map[string]interface{}
	_ = json.Unmarshal(blueprint.Event, &ed)

	title, _ := ed["title"].(string)
	summary, _ := ed["summary"].(string)
	content, _ := ed["content"].(string)
	path, _ := ed["implementation_path"].([]interface{})

	var steps []string
	for _, s := range path {
		if str, ok := s.(string); ok {
			steps = append(steps, str)
		}
	}

	sb := strings.Builder{}
	sb.WriteString("You are the Fabricator. Your goal is to IMPLEMENT the following Blueprint.\n")
	sb.WriteString("You are running in YOLO mode. Do not ask for permission. Just do it.\n\n")

	sb.WriteString("## BLUEPRINT\n")
	sb.WriteString(fmt.Sprintf("**Title**: %s\n", title))
	sb.WriteString(fmt.Sprintf("**Summary**: %s\n", summary))
	sb.WriteString(fmt.Sprintf("**Technical Content**: %s\n", content))
	sb.WriteString("**Implementation Path**:\n")
	for _, s := range steps {
		sb.WriteString(fmt.Sprintf("- %s\n", s))
	}

	return sb.String()
}

// ValidateLogic - Stubs for interface
func (h *FabricatorHandler) ValidateLogic(res agent.AnalysisResult) []agent.Correction { return nil }
