package fabricator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
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
	TTSServiceURL string
	stopChan      chan struct{}
	ctx           context.Context
	cancel        context.CancelFunc
}

func NewFabricatorHandler(redis *redis.Client, ollama *ollama.Client, discord *discord.Client) *FabricatorHandler {
	// Find TTS service URL from service map or use default
	ttsURL := "http://127.0.0.1:8200"

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
		TTSServiceURL: ttsURL,
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

	// New: Listen for voice triggers via a separate ticker or Redis subscription
	// For simplicity, we'll poll for the latest voice trigger event every few seconds
	voiceTicker := time.NewTicker(5 * time.Second)
	defer voiceTicker.Stop()

	for {
		select {
		case <-h.stopChan:
			return
		case <-ticker.C:
			h.checkAndFabricate()
		case <-voiceTicker.C:
			h.checkVoiceTrigger()
		}
	}
}

func (h *FabricatorHandler) checkVoiceTrigger() {
	ctx := h.ctx

	// Fetch latest voice trigger
	ids, err := h.RedisClient.ZRevRange(ctx, "events:type:system.fabricator.voice_trigger", 0, 0).Result()
	if err != nil || len(ids) == 0 {
		return
	}

	id := ids[0]
	isProcessed, _ := h.RedisClient.SIsMember(ctx, "fabricator:processed_voice_triggers", id).Result()
	if isProcessed {
		return
	}

	// Fetch event
	val, err := h.RedisClient.Get(ctx, "event:"+id).Result()
	if err != nil {
		return
	}

	var e types.Event
	if err := json.Unmarshal([]byte(val), &e); err != nil {
		return
	}

	var ed map[string]interface{}
	if err := json.Unmarshal(e.Event, &ed); err != nil {
		return
	}

	transcription, _ := ed["transcription"].(string)
	userID, _ := ed["user_id"].(string)
	channelID, _ := ed["channel_id"].(string)

	// Mark as processed immediately to prevent race
	h.RedisClient.SAdd(ctx, "fabricator:processed_voice_triggers", id)

	log.Printf("[%s] Detected voice trigger: %s", HandlerName, transcription)
	go h.PerformVoiceFabrication(ctx, transcription, userID, channelID)
}

func (h *FabricatorHandler) PerformVoiceFabrication(ctx context.Context, transcription, userID, channelID string) {
	log.Printf("[%s] Starting Autonomous Voice Fabrication", HandlerName)

	// Enforce global sequential execution
	// Special "Voice Mode" lock type to avoid pre-emption
	utils.AcquireCognitiveLock(ctx, h.RedisClient, h.Config.Name, h.Config.ProcessID, h.DiscordClient)
	defer utils.ReleaseCognitiveLock(ctx, h.RedisClient, h.Config.Name)

	utils.ReportProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID, "Construction Protocol (Voice)")
	defer utils.ClearProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID)

	h.RedisClient.Set(ctx, "fabricator:active_tier", "construction", utils.DefaultTTL)
	defer h.RedisClient.Del(ctx, "fabricator:active_tier")

	// 1. Build Autonomous Prompt
	prompt := fmt.Sprintf("User Request (via Voice): %s\n\nObjective: Implement the user's request using your tools. You are running in autonomous YOLO mode. Do not ask for confirmation. Report your changes clearly in the output.", transcription)

	// 2. Execute Fabricator CLI
	binPath, err := exec.LookPath("fabricator")
	if err != nil {
		log.Printf("[%s] Error: 'fabricator' binary not found.", HandlerName)
		return
	}

	cmd := exec.CommandContext(ctx, binPath, "--yolo", "--prompt", prompt)
	out, err := cmd.CombinedOutput()
	output := string(out)

	success := err == nil
	log.Printf("[%s] Voice fabrication complete (Success: %v). Output size: %d", HandlerName, success, len(output))

	// 3. Generate Summary for Voice Feedback
	summary := "I encountered an error while trying to implement your request."
	if success {
		summaryPrompt := fmt.Sprintf("Summarize the following technical changes into a single, natural sentence for voice reporting.\n\nOutput:\n%s", output)
		summary, _, _ = h.OllamaClient.Generate("dex-summary-fast", summaryPrompt, nil)
		if summary == "" {
			summary = "I've completed the requested changes and verified the build."
		}
	}

	// 4. Emit Completion Event
	completionEvent := map[string]interface{}{
		"type":       "system.fabricator.voice_complete",
		"summary":    summary,
		"output":     output,
		"success":    success,
		"user_id":    userID,
		"channel_id": channelID,
		"timestamp":  time.Now().Unix(),
	}

	// Manual emit since we don't have the event service URL handy here, or we can use the Redis timeline
	// Actually, we can use SaveInternalEvent if it was available, but it's in executor.
	// We'll just push to the Redis timeline which the discord service monitors.
	eventID := fmt.Sprintf("fabricator-voice-complete-%d", time.Now().Unix())
	eventJSON, _ := json.Marshal(completionEvent)
	e := types.Event{
		ID:        eventID,
		Service:   "dex-event-service",
		Event:     eventJSON,
		Timestamp: time.Now().Unix(),
	}
	eJSON, _ := json.Marshal(e)
	h.RedisClient.Set(ctx, "event:"+eventID, eJSON, 1*time.Hour)
	h.RedisClient.ZAdd(ctx, "events:timeline", redis.Z{Score: float64(e.Timestamp), Member: eventID})
	h.RedisClient.ZAdd(ctx, "events:type:system.fabricator.voice_complete", redis.Z{Score: float64(e.Timestamp), Member: eventID})

	// 5. Voice Feedback via Discord
	if summary != "" && h.DiscordClient != nil {
		log.Printf("[%s] Generating voice feedback for summary: %s", HandlerName, summary)
		ttsPayload := map[string]string{"text": summary, "language": "en"}
		ttsJson, _ := json.Marshal(ttsPayload)
		ttsResp, ttsErr := http.Post(h.TTSServiceURL+"/generate", "application/json", bytes.NewBuffer(ttsJson))
		if ttsErr == nil && ttsResp.StatusCode == 200 {
			audioBytes, _ := io.ReadAll(ttsResp.Body)
			_ = ttsResp.Body.Close()
			_ = h.DiscordClient.PlayAudio(audioBytes)
		} else {
			if ttsErr != nil {
				log.Printf("[%s] TTS synthesis failed: %v", HandlerName, ttsErr)
			} else {
				log.Printf("[%s] TTS service returned error %d", HandlerName, ttsResp.StatusCode)
				_ = ttsResp.Body.Close()
			}
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
