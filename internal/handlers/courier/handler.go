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
	"github.com/EasterCompany/dex-event-service/internal/model"
	"github.com/EasterCompany/dex-event-service/internal/smartcontext"
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

func NewCourierHandler(redis *redis.Client, modelClient *model.Client, discord *discord.Client, web *web.Client, options interface{}) *CourierHandler {
	ctx, cancel := context.WithCancel(context.Background())
	return &CourierHandler{
		BaseAgent: agent.BaseAgent{
			RedisClient: redis,
			ModelClient: modelClient,
			ChatManager: utils.NewChatContextManager(redis),
			StopTokens:  []string{"<NO_NEW_DATA/>", "<NO_RESULTS/>"},
		},
		Config: agent.AgentConfig{
			Name:      "Courier",
			ProcessID: "system-courier",
			Models: map[string]string{
				"researcher": "dex-courier-researcher",
				"compressor": "dex-courier-compressor",
			},
			ProtocolAliases: map[string]string{
				"researcher": "Researcher",
				"compressor": "Compressor",
			},
			Cooldowns: map[string]int{
				"researcher": 300, // 5 minutes
				"compressor": 300, // 5 minutes
			},
			IdleRequirement: 300,
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
	go h.PerformWaterfall(ctx)
	return nil, "", nil
}

func (h *CourierHandler) runWorker() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-h.stopChan:
			return
		case <-ticker.C:
			h.checkAndExecute()
		}
	}
}

func (h *CourierHandler) checkAndExecute() {
	ctx := h.ctx
	if utils.IsSystemPaused(ctx, h.RedisClient) {
		return
	}

	if h.IsActuallyBusy(ctx, h.Config.ProcessID) {
		return
	}

	now := time.Now().Unix()

	// RULE: No agent can run any protocols unless ALL protocols within it are "READY"
	for protocol, cooldown := range h.Config.Cooldowns {
		lastRun, _ := h.RedisClient.Get(ctx, fmt.Sprintf("courier:last_run:%s", protocol)).Int64()
		if now-lastRun < int64(cooldown) {
			return
		}
	}

	// 2. Check if any tasks are actually due
	tasks, err := h.ChoreStore.GetAll(ctx)
	if err != nil || len(tasks) == 0 {
		return
	}

	anyDue := false
	for _, t := range tasks {
		if t.Status == chores.ChoreStatusActive && h.isTaskDue(t) {
			anyDue = true
			break
		}
	}

	if anyDue {
		go h.PerformWaterfall(ctx)
	}
}

func (h *CourierHandler) PerformWaterfall(ctx context.Context) {
	log.Printf("[%s] Starting Courier Waterfall Run", HandlerName)

	// RULE: Immediately "claim" the run by setting the last_run timestamps to now.
	// This prevents the 1-minute ticker from spawning redundant goroutines while this one is waiting for the lock.
	now := time.Now().Unix()
	for protocol := range h.Config.Cooldowns {
		h.RedisClient.Set(ctx, fmt.Sprintf("courier:last_run:%s", protocol), now, 0)
	}

	utils.AcquireCognitiveLock(ctx, h.RedisClient, h.Config.Name, h.Config.ProcessID, h.DiscordClient)
	defer utils.ReleaseCognitiveLock(ctx, h.RedisClient, h.Config.Name)

	// ... rest of the function ...

	// RULE: Every courier agent run MUST go researcher -> compressor
	// If any tier skips or fails, the waterfall STOPS immediately.

	// TIER 1: RESEARCHER

	h.RedisClient.Set(ctx, "courier:active_tier", "researcher", utils.DefaultTTL)

	utils.ReportProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID, "Researcher Protocol")

	_, inferenceCount, err := h.PerformResearch(ctx)

	if err != nil || inferenceCount == 0 {

		log.Printf("[%s] Waterfall aborted: Researcher found no work or failed.", HandlerName)
		h.cleanupRun(ctx)
		return
	}
	h.RedisClient.Set(ctx, "courier:last_run:researcher", time.Now().Unix(), 0)

	// TIER 2: COMPRESSOR
	h.RedisClient.Set(ctx, "courier:active_tier", "compressor", utils.DefaultTTL)
	utils.ReportProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID, "Compressor Protocol")
	err = h.PerformCompression(ctx)
	if err == nil {
		h.RedisClient.Set(ctx, "courier:last_run:compressor", time.Now().Unix(), 0)
	}

	h.cleanupRun(ctx)
}

func (h *CourierHandler) cleanupRun(ctx context.Context) {
	h.RedisClient.Del(ctx, "courier:active_tier")
	utils.ClearProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID)
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

func (h *CourierHandler) PerformResearch(ctx context.Context) ([]agent.AnalysisResult, int, error) {
	tasks, err := h.ChoreStore.GetAll(ctx)
	if err != nil || len(tasks) == 0 {
		return nil, 0, nil
	}

	var dueTasks []*chores.Chore
	for _, t := range tasks {
		if t.Status == chores.ChoreStatusActive && h.isTaskDue(t) {
			dueTasks = append(dueTasks, t)
		}
	}

	if len(dueTasks) == 0 {
		return nil, 0, nil
	}

	log.Printf("[%s] Starting Courier Research (%d tasks due)", HandlerName, len(dueTasks))

	var totalResults []agent.AnalysisResult
	inferenceCount := 0

	for _, task := range dueTasks {
		res, auditID, err := h.executeTask(ctx, task)
		if err == nil && len(res) > 0 {
			totalResults = append(totalResults, res...)
			h.deliverResults(ctx, task, res[0], auditID)
			_ = h.ChoreStore.MarkRun(ctx, task.ID, nil) // Update last run
			inferenceCount++
		}
	}

	return totalResults, inferenceCount, nil
}

func (h *CourierHandler) executeTask(ctx context.Context, task *chores.Chore) ([]agent.AnalysisResult, string, error) {
	statusMsg := fmt.Sprintf("Researching: %s", task.NaturalInstruction)
	log.Printf("[%s] Executing Task: %s", HandlerName, task.NaturalInstruction)
	utils.ReportProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID, statusMsg)

	queryModel := h.Config.Models["researcher"]
	now := time.Now().Format("2006-01-02")

	// Helper to perform search and scrape
	performSearchAndScrape := func(query string) (string, []string, int) {
		targetURL := task.ExecutionPlan.EntryURL
		var localWebData strings.Builder
		var localSourceLinks []string
		seenURLs := make(map[string]bool)
		totalContentLen := 0

		if targetURL != "" {
			scraped, _ := h.WebClient.PerformScrape(ctx, targetURL)
			if scraped != nil {
				localWebData.WriteString(fmt.Sprintf("# CONTENT FROM TARGET URL: %s\n\n%s\n\n", targetURL, scraped.Content))
				localSourceLinks = append(localSourceLinks, targetURL)
				seenURLs[targetURL] = true
				totalContentLen += len(scraped.Content)
			}
		} else {
			results, _ := h.WebClient.PerformSearch(ctx, query, 5)
			localWebData.WriteString(fmt.Sprintf("# SEARCH RESULTS FOR: %s\n\n", query))

			for i, r := range results {
				if len(localSourceLinks) >= 3 {
					break
				}
				cleanURL := strings.TrimSuffix(r.URL, "/")
				if seenURLs[cleanURL] {
					continue
				}
				seenURLs[cleanURL] = true

				log.Printf("[%s] Scraping result %d: %s", HandlerName, i+1, r.URL)
				scraped, _ := h.WebClient.PerformScrape(ctx, r.URL)

				content := ""
				if scraped != nil {
					content = scraped.Content
				}

				if len(content) > 100 {
					localWebData.WriteString(fmt.Sprintf("## SOURCE %d: %s (%s)\n\n%s\n\n---\n\n", i+1, r.Title, r.URL, content))
					localSourceLinks = append(localSourceLinks, r.URL)
					totalContentLen += len(content)
				} else {
					localWebData.WriteString(fmt.Sprintf("## SOURCE %d (Snippet): %s (%s)\n\n%s\n\n---\n\n", i+1, r.Title, r.URL, r.Snippet))
					localSourceLinks = append(localSourceLinks, r.URL)
					totalContentLen += len(r.Snippet)
				}
			}
		}
		return localWebData.String(), localSourceLinks, totalContentLen
	}

	// 1. Initial Query Generation
	queryPrompt := fmt.Sprintf("Current Date: %s. You are an intelligence officer. Generate a search query for: %s.\n\nRULES:\n1. Use natural language keywords + date ranges if needed.\n2. AVOID complex 'site:' operators.\n3. Output ONLY the query string.", now, task.NaturalInstruction)
	queryResp, err := h.ModelClient.Chat(ctx, queryModel, []model.Message{{Role: "user", Content: queryPrompt}})
	searchQuery := task.NaturalInstruction
	if err == nil {
		searchQuery = strings.Trim(queryResp.Content, "\" \n\r")
	}
	log.Printf("[%s] Initial Query: %s", HandlerName, searchQuery)

	// 2. Initial Search
	webDataStr, sourceLinks, contentLen := performSearchAndScrape(searchQuery)

	// 3. Quality Check & Re-Query
	// Heuristic: If we have fewer than 2 sources OR very little content (< 1000 chars), try again.
	if len(sourceLinks) < 2 || contentLen < 1000 {
		log.Printf("[%s] Initial search quality low (Sources: %d, Content: %d). Triggering Re-Query...", HandlerName, len(sourceLinks), contentLen)

		reQueryPrompt := fmt.Sprintf("Current Date: %s. Your previous search query '%s' yielded poor results (only %d sources found). \n\nTask: %s\n\nGenerate a NEW, REFINED search query to find better information. Try broader terms or different synonyms. Output ONLY the query string.", now, searchQuery, len(sourceLinks), task.NaturalInstruction)
		reQueryResp, err := h.ModelClient.Chat(ctx, queryModel, []model.Message{{Role: "user", Content: reQueryPrompt}})

		if err == nil {
			newQuery := strings.Trim(reQueryResp.Content, "\" \n\r")
			log.Printf("[%s] Refined Query: %s", HandlerName, newQuery)

			// Retry Search
			retryWebData, retryLinks, retryLen := performSearchAndScrape(newQuery)

			// If retry yielded ANY data, append it (don't replace, we want to accumulate knowledge)
			if retryLen > 0 {
				webDataStr += "\n\n# REFINED SEARCH DATA\n" + retryWebData
				// Merge links uniquely
				for _, link := range retryLinks {
					exists := false
					for _, existing := range sourceLinks {
						if existing == link {
							exists = true
							break
						}
					}
					if !exists {
						sourceLinks = append(sourceLinks, link)
					}
				}
			}
		}
	}

	// 2.5 Circuit Breaker
	if len(sourceLinks) == 0 {
		log.Printf("[%s] Research Aborted: No valid sources found.", HandlerName)
		utils.ReportProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID, "Failed: No Data Found")
		_ = h.ChoreStore.MarkRun(ctx, task.ID, nil)
		return nil, "", fmt.Errorf("no data found")
	}

	// 3. Final Synthesis
	utils.ReportProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID, "Synthesizing Report")

	sourcesBlock := "\n\n### SOURCES\n"
	for _, link := range sourceLinks {
		sourcesBlock += fmt.Sprintf("- %s\n", link)
	}

	systemPrompt := "You are the Courier Agent's Researcher Protocol. Your goal is to act as a high-fidelity intelligence analyst. Analyze the provided web data and sources to create a comprehensive, factual, and surgically accurate report.\n\nFILTERING RULES:\n1. IGNORE repetitive 'Latest Updates' feed lists, navigation menus, or sidebar widgets.\n2. IGNORE local crime stories, celebrity gossip, or minor human interest stories unless they have geopolitical significance.\n3. IGNORE marketing content or blog posts that are not primary news reporting.\n4. IGNORE website metadata, 'About Us' sections, copyright notices, subscription prompts, and navigation headers.\n5. Prioritize geopolitical events, economic indicators, and major policy changes.\n\nAlways include a 'Sources' section at the end with the provided links."
	inputContext := fmt.Sprintf("## USER INSTRUCTION\n%s\n\n## RESEARCH DATA\n%s%s", task.NaturalInstruction, webDataStr, sourcesBlock)

	inputContext = utils.NormalizeWhitespace(inputContext)
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

	embed := &discord.Embed{
		Title:       task.NaturalInstruction,
		Description: res.Content,
		Color:       0x00b0f4, // Research Teal
		Footer: &discord.EmbedFooter{
			Text: fmt.Sprintf("Courier Protocol • %s", time.Now().Format("2006-01-02 15:04")),
		},
	}

	if _, err := h.DiscordClient.PostMessageComplex(recipient, "", embed); err != nil {
		log.Printf("[%s] Failed to send research results to %s: %v", HandlerName, recipient, err)
	}
}

// PerformCompression scans all active channel timelines and triggers summaries for those with enough new data.
func (h *CourierHandler) PerformCompression(ctx context.Context) error {
	log.Printf("[%s] Starting Context Compression Protocol", HandlerName)

	// 1. Identify all channels with event timelines
	keys, err := h.RedisClient.Keys(ctx, "events:channel:*").Result()
	if err != nil {
		return err
	}

	compressedCount := 0
	for _, key := range keys {
		channelID := strings.TrimPrefix(key, "events:channel:")

		// Trigger asynchronous summary update for each channel
		// UpdateSummary handles its own internal batching/threshold logic (15+ new events)
		go smartcontext.UpdateSummary(context.Background(), h.RedisClient, h.ModelClient, h.DiscordClient, channelID, h.Config.Models["compressor"], smartcontext.CachedSummary{}, nil, nil)
		compressedCount++
	}

	h.RedisClient.Set(ctx, "courier:last_run:compressor", time.Now().Unix(), 0)
	log.Printf("[%s] Context Compression Protocol complete. Initiated summary checks for %d channels.", HandlerName, compressedCount)
	return nil
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
