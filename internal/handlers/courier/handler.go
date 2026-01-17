package courier

import (
	"context"
	"fmt"
	"log"
	"strings"
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
				"researcher": "dex-researcher-model",
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
				"Summary", "Content", "Status", "Sources",
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
	utils.AcquireCognitiveLock(ctx, h.RedisClient, h.Config.Name, h.Config.ProcessID, h.DiscordClient)
	defer utils.ReleaseCognitiveLock(ctx, h.RedisClient, h.Config.Name)

	h.RedisClient.Set(ctx, "courier:active_tier", "researcher", utils.DefaultTTL)
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
			h.deliverResults(ctx, task, res[0], auditID)
			_ = h.ChoreStore.MarkRun(ctx, task.ID, nil) // Update last run
		}
	}

	h.RedisClient.Set(ctx, "courier:last_run:researcher", time.Now().Unix(), 0)
	utils.RecordProcessOutcome(ctx, h.RedisClient, h.Config.ProcessID, "success")

	return totalResults, lastAuditID, nil
}

func (h *CourierHandler) executeTask(ctx context.Context, task *chores.Chore) ([]agent.AnalysisResult, string, error) {
	log.Printf("[%s] Executing Task: %s", HandlerName, task.NaturalInstruction)

	// 1. Generate Optimized Search Query
	queryModel := h.Config.Models["researcher"]
	queryPrompt := fmt.Sprintf("You are an intelligence officer. Generate a highly specific, bot-friendly search query for the following research task: %s. Output ONLY the query string, no quotes or explanations.", task.NaturalInstruction)

	// Quick one-off chat for query optimization
	queryResp, err := h.OllamaClient.Chat(ctx, queryModel, []ollama.Message{{Role: "user", Content: queryPrompt}})
	searchQuery := task.NaturalInstruction
	if err == nil {
		searchQuery = strings.Trim(queryResp.Content, "\" \n\r")
		log.Printf("[%s] Optimized Query: %s", HandlerName, searchQuery)
	}

	// 2. Perform Web Search
	targetURL := task.ExecutionPlan.EntryURL
	var webData strings.Builder
	var sourceLinks []string

	if targetURL != "" {
		scraped, _ := h.WebClient.PerformScrape(ctx, targetURL)
		if scraped != nil {
			webData.WriteString(fmt.Sprintf("# CONTENT FROM TARGET URL: %s\n\n%s\n\n", targetURL, scraped.Content))
			sourceLinks = append(sourceLinks, targetURL)
		}
	} else {
		results, _ := h.WebClient.PerformSearch(ctx, searchQuery, 5)
		webData.WriteString(fmt.Sprintf("# SEARCH RESULTS FOR: %s\n\n", searchQuery))

		for i, r := range results {
			if i >= 5 {
				break
			} // Hard limit
			log.Printf("[%s] Scraping result %d: %s", HandlerName, i+1, r.URL)

			// Indicate progress in process state
			utils.ReportProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID, fmt.Sprintf("Scraping %d/5: %s", i+1, r.Title))

			scraped, _ := h.WebClient.PerformScrape(ctx, r.URL)
			if scraped != nil && len(scraped.Content) > 100 {
				webData.WriteString(fmt.Sprintf("## SOURCE %d: %s (%s)\n\n%s\n\n---\n\n", i+1, r.Title, r.URL, scraped.Content))
				sourceLinks = append(sourceLinks, r.URL)
			} else {
				// Fallback to snippet if scrape fails
				webData.WriteString(fmt.Sprintf("## SOURCE %d (Snippet): %s (%s)\n\n%s\n\n---\n\n", i+1, r.Title, r.URL, r.Snippet))
				sourceLinks = append(sourceLinks, r.URL)
			}
		}
	}

	// 3. Final Synthesis Cognitive Loop

	utils.ReportProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID, "Synthesizing Report")

	sourcesBlock := "\n\n### SOURCES\n"

	for _, link := range sourceLinks {

		sourcesBlock += fmt.Sprintf("- %s\n", link)

	}

	systemPrompt := "You are the Courier Agent's Researcher Protocol. Your goal is to act as a high-fidelity intelligence analyst. Analyze the provided web data and sources to create a comprehensive, factual, and surgically accurate report. Always include a 'Sources' section at the end with the provided links."

	inputContext := fmt.Sprintf("## USER INSTRUCTION\n%s\n\n## RESEARCH DATA\n%s%s", task.NaturalInstruction, webData.String(), sourcesBlock)

	sessionID := fmt.Sprintf("research-%s-%d", task.ID, time.Now().Unix())

	return h.RunCognitiveLoop(ctx, h, "researcher", h.Config.Models["researcher"], sessionID, systemPrompt, inputContext, 1)
}

func (h *CourierHandler) deliverResults(ctx context.Context, task *chores.Chore, res agent.AnalysisResult, auditID string) {
	// Deliver to all recipients
	for _, recipient := range task.Recipients {
		h.deliverToRecipient(ctx, recipient, task, res, auditID)
	}
}

func (h *CourierHandler) deliverToRecipient(ctx context.Context, recipient string, task *chores.Chore, res agent.AnalysisResult, auditID string) {
	if recipient == "dexter" {
		// Deliver to Event Timeline as a High-Visibility Notification
		payload := map[string]interface{}{
			"title":             fmt.Sprintf("Research Result: %s", res.Title),
			"body":              res.Content,
			"summary":           res.Summary,
			"content":           res.Content,
			"category":          "research",
			"priority":          "normal",
			"type":              "system.notification.generated",
			"protocol":          "researcher",
			"audit_event_id":    auditID,
			"read":              false,
			"related_event_ids": []string{},
		}
		_, _ = utils.SendEvent(ctx, h.RedisClient, HandlerName, "system.notification.generated", payload)
		return
	}

	if h.DiscordClient == nil {
		return
	}

	body := res.Content
	msg := fmt.Sprintf("📦 **Research Task Completed**\n\n**Task:** %s\n\n%s", task.NaturalInstruction, body)
	_, _ = h.DiscordClient.PostMessage(recipient, msg)
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
