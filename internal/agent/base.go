package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/EasterCompany/dex-event-service/internal/ollama"
	"github.com/EasterCompany/dex-event-service/utils"
	"github.com/redis/go-redis/v9"
)

// BaseAgent provides shared utilities for all agents.
type BaseAgent struct {
	RedisClient  *redis.Client
	OllamaClient *ollama.Client
	ChatManager  *utils.ChatContextManager
}

// IsActuallyBusy checks if the system is currently processing tasks.
func (b *BaseAgent) IsActuallyBusy(ctx context.Context, selfProcessID string) bool {
	keys, err := b.RedisClient.Keys(ctx, "process:info:*").Result()
	if err != nil {
		return false
	}

	for _, k := range keys {
		// Ignore ourselves
		if strings.HasSuffix(k, ":"+selfProcessID) {
			continue
		}
		// If ANY other process exists, we are busy
		return true
	}

	return false
}

// CleanupBusyCount resyncs the global busy metric.
func (b *BaseAgent) CleanupBusyCount(ctx context.Context) {
	keys, _ := b.RedisClient.Keys(ctx, "process:info:*").Result()
	actualCount := len(keys)

	currentCount, _ := b.RedisClient.Get(ctx, "system:busy_ref_count").Int()
	if actualCount != currentCount {
		if actualCount > 0 && currentCount == 0 {
			utils.TransitionToBusy(ctx, b.RedisClient)
		} else if actualCount == 0 && currentCount > 0 {
			utils.TransitionToIdle(ctx, b.RedisClient)
		}
		b.RedisClient.Set(ctx, "system:busy_ref_count", actualCount, 0)
	}
}

// RunCognitiveLoop executes the 3-attempt chat process with retry logic.
func (b *BaseAgent) RunCognitiveLoop(ctx context.Context, model, sessionID, systemPrompt, inputContext string, limit int) ([]AnalysisResult, error) {
	chatHistory, _ := b.ChatManager.LoadHistory(ctx, sessionID)
	if len(chatHistory) == 0 || chatHistory[0].Role != "system" {
		chatHistory = append([]ollama.Message{{Role: "system", Content: systemPrompt}}, chatHistory...)
	} else {
		chatHistory[0].Content = systemPrompt
	}

	newUserMsg := ollama.Message{Role: "user", Content: inputContext}
	currentTurnHistory := append(chatHistory, newUserMsg)

	maxRetries := 3
	var lastError error

	for i := 0; i < maxRetries; i++ {
		tCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		respMsg, err := b.OllamaClient.Chat(tCtx, model, currentTurnHistory)
		cancel()

		if err != nil {
			lastError = err
			b.RedisClient.Incr(ctx, "system:metrics:model:"+model+":failures")
			continue
		}

		results := b.ParseAnalysisResults(respMsg.Content, limit)
		if len(results) > 0 || strings.Contains(respMsg.Content, "No significant insights found") || strings.Contains(respMsg.Content, "<NO_ISSUES/>") {
			_ = b.ChatManager.AppendMessage(ctx, sessionID, newUserMsg)
			_ = b.ChatManager.AppendMessage(ctx, sessionID, respMsg)
			return results, nil
		}

		// Malformed response handling
		b.RedisClient.Incr(ctx, "system:metrics:model:"+model+":failures")
		currentTurnHistory = append(currentTurnHistory, respMsg)
		currentTurnHistory = append(currentTurnHistory, ollama.Message{
			Role:    "user",
			Content: "SYSTEM ERROR: Your response did not follow the strict 'Dexter Report' format. Fix it immediately.",
		})
	}

	return nil, fmt.Errorf("max retries reached or cognitive failure: %v", lastError)
}

// ParseAnalysisResults handles multi-report responses with an optional limit.
func (b *BaseAgent) ParseAnalysisResults(response string, limit int) []AnalysisResult {
	if strings.Contains(response, "No significant insights found") || strings.Contains(response, "<NO_ISSUES/>") {
		return nil
	}
	var results []AnalysisResult
	re := regexp.MustCompile(`(?m)^\s*---\s*$`)
	for _, section := range re.Split(response, -1) {
		if limit > 0 && len(results) >= limit {
			break
		}
		res := b.ParseSingleMarkdownReport(section)
		// Validation: Must have Title AND (Summary OR Content)
		if res.Title != "" && (res.Summary != "" || res.Content != "") {
			results = append(results, res)
		}
	}
	return results
}

// ParseSingleMarkdownReport implements strict section-based parsing.
func (b *BaseAgent) ParseSingleMarkdownReport(input string) AnalysisResult {
	var res AnalysisResult
	lines := strings.Split(input, "\n")
	var currentSection string
	var contentLines, pathLines, summaryLines []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// Title handles top-level header
		if strings.HasPrefix(trimmed, "# ") {
			res.Title = strings.TrimPrefix(trimmed, "# ")
			if strings.Contains(strings.ToUpper(res.Title), "[BLUEPRINT]") {
				res.Type = "blueprint"
				res.Title = strings.TrimSpace(strings.Replace(strings.ToUpper(res.Title), "[BLUEPRINT]", "", 1))
			}
			continue
		}

		lower := strings.ToLower(trimmed)
		// Metadata fields
		if strings.HasPrefix(lower, "**priority**:") {
			res.Priority = strings.TrimSpace(trimmed[strings.Index(trimmed, ":")+1:])
			continue
		}
		if strings.HasPrefix(lower, "**category**:") {
			res.Category = strings.TrimSpace(trimmed[strings.Index(trimmed, ":")+1:])
			continue
		}
		if strings.HasPrefix(lower, "**affected**:") || strings.HasPrefix(lower, "**affected services**:") {
			res.AffectedServices = strings.Split(strings.TrimSpace(trimmed[strings.Index(trimmed, ":")+1:]), ",")
			continue
		}

		// Section headers
		if strings.Contains(lower, "summary") {
			currentSection = "summary"
			continue
		}
		if strings.Contains(lower, "content") || strings.Contains(lower, "insight") || strings.Contains(lower, "body") {
			currentSection = "content"
			continue
		}
		if strings.Contains(lower, "implementation path") || strings.Contains(lower, "proposed steps") {
			currentSection = "path"
			continue
		}

		// Strict header parsing - any unknown header stops capture
		if strings.HasPrefix(trimmed, "#") {
			currentSection = ""
			continue
		}

		// Content capture based on active section
		switch currentSection {
		case "summary":
			summaryLines = append(summaryLines, trimmed)
		case "content":
			contentLines = append(contentLines, trimmed)
		case "path":
			if strings.HasPrefix(trimmed, "-") || strings.HasPrefix(trimmed, "*") || (len(trimmed) > 2 && trimmed[1] == '.') {
				pathLines = append(pathLines, trimmed)
			}
		}
	}

	res.Summary = strings.Join(summaryLines, " ")
	res.Content = strings.Join(contentLines, "\n")
	res.ImplementationPath = pathLines

	// Default Body logic (will be refined by specific agent tiers)
	res.Body = res.Content
	if res.Body == "" {
		res.Body = res.Summary
	}

	return res
}
