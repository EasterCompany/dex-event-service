package courier

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/EasterCompany/dex-event-service/internal/agent"
	"github.com/EasterCompany/dex-event-service/internal/chores"
	"github.com/EasterCompany/dex-event-service/internal/discord"
	"github.com/EasterCompany/dex-event-service/internal/ollama"
	"github.com/EasterCompany/dex-event-service/internal/web"
	"github.com/EasterCompany/dex-event-service/utils"
	"github.com/redis/go-redis/v9"
)

const (
	HandlerName = "courier-handler"
)

type CourierHandler struct {
	agent.BaseAgent
	Config        agent.AgentConfig
	DiscordClient *discord.Client
	WebClient     *web.Client
	ChoreStore    *chores.Store
	stopChan      chan struct{}
	ctx           context.Context
	cancel        context.CancelFunc
}

func NewCourierHandler(redis *redis.Client, ollama *ollama.Client, discord *discord.Client, web *web.Client, options interface{}) *CourierHandler {
	ctx, cancel := context.WithCancel(context.Background())
	return &CourierHandler{
		BaseAgent: agent.BaseAgent{
			RedisClient:  redis,
			OllamaClient: ollama,
			ChatManager:  utils.NewChatContextManager(redis),
			StopTokens:   []string{"<NO_NEW_DATA/>", "<NO_RESULTS/>"},
		},
		Config: agent.AgentConfig{
			Name:      "Courier",
			ProcessID: "system-courier",
			Models: map[string]string{
				"researcher": "dex-scraper-model",
			},
			ProtocolAliases: map[string]string{
				"researcher": "Researcher",
			},
			Cooldowns: map[string]int{
				"researcher": 300, // 5 minutes protocol cooldown
			},
			IdleRequirement: 60, // Courier is more aggressive
			DateTimeAware:   true,
			EnforceMarkdown: true,
			RequiredSections: []string{
				"Summary", "Content",
			},
		},
		DiscordClient: discord,
		WebClient:     web,
		ChoreStore:    chores.NewStore(redis),
		ctx:           ctx,
		cancel:        cancel,
	}
}

func (h *CourierHandler) Init(ctx context.Context) error {
	h.stopChan = make(chan struct{})
	go h.runWorker()
	log.Printf("[%s] Background worker started.", HandlerName)
	return nil
}

func (h *CourierHandler) Close() error {
	if h.cancel != nil {
		h.cancel()
	}
	if h.stopChan != nil {
		close(h.stopChan)
	}
	utils.ClearProcess(context.Background(), h.RedisClient, h.DiscordClient, h.Config.ProcessID)
	return nil
}

func (h *CourierHandler) GetConfig() agent.AgentConfig {
	return h.Config
}

func (h *CourierHandler) Run(ctx context.Context) ([]agent.AnalysisResult, string, error) {
	return h.PerformResearch(ctx)
}

func (h *CourierHandler) runWorker() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-h.stopChan:
			return
		case <-ticker.C:
			h.checkAndResearch()
		}
	}
}

func (h *CourierHandler) checkAndResearch() {
	ctx := h.ctx
	now := time.Now().Unix()

	// 1. System Pause Check
	if utils.IsSystemPaused(ctx, h.RedisClient) {
		return
	}

	// 2. No ongoing processes (True Busy Check)
	if h.IsActuallyBusy(ctx, h.Config.ProcessID) {
		return
	}

	// 3. Protocol Cooldown
	lastRun, _ := h.RedisClient.Get(ctx, "courier:last_run:researcher").Int64()
	if now-lastRun < int64(h.Config.Cooldowns["researcher"]) {
		return
	}

	// 4. Check if any tasks are actually due
	tasks, err := h.ChoreStore.GetAll(ctx)
	if err != nil || len(tasks) == 0 {
		return
	}

	anyDue := false
	for _, t := range tasks {
		if t.Status != chores.ChoreStatusActive {
			continue
		}
		if h.isTaskDue(t) {
			anyDue = true
			break
		}
	}

	if !anyDue {
		return
	}

	// Trigger Research
	_, _, _ = h.PerformResearch(ctx)
}

func (h *CourierHandler) isTaskDue(t *chores.Chore) bool {
	if t.LastRun == 0 {
		return true
	}

	now := time.Now().Unix()
	var interval int64

	switch t.Schedule {
	case "every_1h":
		interval = 3600
	case "every_6h":
		interval = 21600
	case "every_12h":
		interval = 43200
	case "every_24h":
		interval = 86400
	case "every_168h":
		interval = 604800
	default:
		interval = 86400
	}

	return now-t.LastRun >= interval
}

func (h *CourierHandler) PerformResearch(ctx context.Context) ([]agent.AnalysisResult, string, error) {
	tasks, err := h.ChoreStore.GetAll(ctx)
	if err != nil || len(tasks) == 0 {
		return nil, "", nil
	}

	var dueTasks []*chores.Chore
	for _, t := range tasks {
		if t.Status == chores.ChoreStatusActive && h.isTaskDue(t) {
			dueTasks = append(dueTasks, t)
		}
	}

	if len(dueTasks) == 0 {
		return nil, "", nil
	}

	log.Printf("[%s] Starting Courier Research (%d tasks due)", HandlerName, len(dueTasks))

	// Enforce global sequential execution
	utils.AcquireCognitiveLock(ctx, h.RedisClient, h.Config.Name)
	defer utils.ReleaseCognitiveLock(ctx, h.RedisClient, h.Config.Name)

	h.RedisClient.Set(ctx, "courier:active_tier", "Working (Researcher)", utils.DefaultTTL)
	defer h.RedisClient.Del(ctx, "courier:active_tier")
	utils.ReportProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID, "Researcher Protocol")
	defer utils.ClearProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID)

	var totalResults []agent.AnalysisResult
	var lastAuditID string

	for _, task := range dueTasks {
		res, auditID, err := h.executeTask(ctx, task)
		lastAuditID = auditID
		if err == nil && len(res) > 0 {
			totalResults = append(totalResults, res...)
			h.deliverResults(ctx, task, res[0])
			_ = h.ChoreStore.MarkRun(ctx, task.ID, nil) // Update last run
		}
	}

	h.RedisClient.Set(ctx, "courier:last_run:researcher", time.Now().Unix(), 0)
	utils.RecordProcessOutcome(ctx, h.RedisClient, h.Config.ProcessID, "success")

	return totalResults, lastAuditID, nil
}

func (h *CourierHandler) executeTask(ctx context.Context, task *chores.Chore) ([]agent.AnalysisResult, string, error) {
	log.Printf("[%s] Executing Task: %s", HandlerName, task.NaturalInstruction)

	// 1. Scraping / Web Search
	targetURL := task.ExecutionPlan.EntryURL
	searchQuery := task.NaturalInstruction
	if task.ExecutionPlan.SearchQuery != "" {
		searchQuery = task.ExecutionPlan.SearchQuery
	}

	var webData string
	if targetURL != "" {
		scraped, _ := h.WebClient.PerformScrape(ctx, targetURL)
		if scraped != nil {
			webData = scraped.Content
		}
	} else {
		results, _ := h.WebClient.PerformSearch(ctx, searchQuery, 5)
		webData = fmt.Sprintf("Search Results for: %s\n\n", searchQuery)
		for _, r := range results {
			webData += fmt.Sprintf("Title: %s\nURL: %s\nSnippet: %s\n---\n", r.Title, r.URL, r.Snippet)
		}
	}

	// 2. Cognitive Loop
	systemPrompt := "You are the Courier Agent's Researcher Protocol. Your goal is to analyze web data and provide a concise, high-fidelity report based on the user's instructions."
	inputContext := fmt.Sprintf("## USER INSTRUCTION\n%s\n\n## RESEARCH DATA\n%s", task.NaturalInstruction, webData)
	sessionID := fmt.Sprintf("research-%s-%d", task.ID, time.Now().Unix())

	return h.RunCognitiveLoop(ctx, h, "researcher", h.Config.Models["researcher"], sessionID, systemPrompt, inputContext, 1)
}

func (h *CourierHandler) deliverResults(ctx context.Context, task *chores.Chore, res agent.AnalysisResult) {
	if task.OwnerID == "dexter" {
		// Deliver to Event Timeline
		payload := map[string]interface{}{
			"title":    fmt.Sprintf("Research Result: %s", res.Title),
			"body":     res.Content,
			"summary":  res.Summary,
			"category": "research",
			"priority": "normal",
			"type":     "system.research.result",
		}
		_, _ = utils.SendEvent(ctx, h.RedisClient, HandlerName, "system.research.result", payload)
	} else {
		// Deliver to Discord
		msg := fmt.Sprintf("📦 **Research Task Completed**\n\n**Task:** %s\n\n%s", task.NaturalInstruction, res.Content)
		if h.DiscordClient != nil {
			_, _ = h.DiscordClient.PostMessage(task.OwnerID, msg)
		}
	}
}

func (h *CourierHandler) ValidateLogic(res agent.AnalysisResult) []agent.Correction {
	var corrections []agent.Correction
	if len(res.Content) < 50 {
		corrections = append(corrections, agent.Correction{
			Type: "LOGIC", Guidance: "The research output is too short. Provide more detailed insights from the data.", Mandatory: true,
		})
	}
	return corrections
}
