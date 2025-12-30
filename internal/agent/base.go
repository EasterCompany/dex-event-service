package agent

import (
	"context"
	"encoding/json"
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
	StopTokens   []string
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
// It ALWAYS produces a system.analysis.audit event regardless of outcome.
func (b *BaseAgent) RunCognitiveLoop(ctx context.Context, agentName, tierName, model, sessionID, systemPrompt, inputContext string, limit int, dateTimeAware bool, enforceMarkdown bool) ([]AnalysisResult, error) {
	startTime := time.Now()

	// Inject current date/time if aware
	if dateTimeAware {
		now := time.Now().Format("Monday, 02 Jan 2006 15:04:05 MST")
		inputContext = fmt.Sprintf("### CURRENT DATE/TIME\n%s\n\n%s", now, inputContext)
	}

	var results []AnalysisResult
	var lastError error
	var rawOutput string
	attempts := 0
	success := false

	defer func() {
		duration := time.Since(startTime).String()
		errStr := ""
		if lastError != nil {
			errStr = lastError.Error()
		}

		audit := AuditPayload{
			Type:          "system.analysis.audit",
			AgentName:     agentName,
			Tier:          tierName,
			Model:         model,
			InputContext:  inputContext,
			RawOutput:     rawOutput,
			ParsedResults: results,
			Error:         errStr,
			Duration:      duration,
			Timestamp:     time.Now().Unix(),
			Attempts:      attempts,
			Success:       success,
		}

		// Convert to map for SendEvent
		var auditMap map[string]interface{}
		auditJSON, _ := json.Marshal(audit)
		_ = json.Unmarshal(auditJSON, &auditMap)

		utils.SendEvent(ctx, b.RedisClient, "dex-event-service", "system.analysis.audit", auditMap)
	}()

	chatHistory, _ := b.ChatManager.LoadHistory(ctx, sessionID)
	if systemPrompt != "" {
		if len(chatHistory) == 0 || chatHistory[0].Role != "system" {
			chatHistory = append([]ollama.Message{{Role: "system", Content: systemPrompt}}, chatHistory...)
		} else {
			chatHistory[0].Content = systemPrompt
		}
	}

	newUserMsg := ollama.Message{Role: "user", Content: inputContext}
	currentTurnHistory := append(chatHistory, newUserMsg)

	maxRetries := 3

	for i := 0; i < maxRetries; i++ {
		attempts++
		b.RedisClient.Incr(ctx, "system:metrics:model:"+model+":attempts")

		tCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		respMsg, err := b.OllamaClient.Chat(tCtx, model, currentTurnHistory)
		cancel()

		if err != nil {
			lastError = err
			b.RedisClient.Incr(ctx, "system:metrics:model:"+model+":failures")
			continue
		}

		rawOutput = respMsg.Content
		results = b.ParseAnalysisResults(respMsg.Content, limit)

		// 1. Markdown Enforcement
		if enforceMarkdown {
			mdIssues := b.ValidateMarkdown(respMsg.Content)
			if len(mdIssues) > 0 {
				b.RedisClient.Incr(ctx, "system:metrics:model:"+model+":failures")
				currentTurnHistory = append(currentTurnHistory, respMsg)
				issueStr := strings.Join(mdIssues, "\n- ")
				currentTurnHistory = append(currentTurnHistory, ollama.Message{
					Role:    "user",
					Content: fmt.Sprintf("SYSTEM ERROR: Your markdown is invalid. Please fix the following issues and resubmit your full report:\n- %s", issueStr),
				})
				continue
			}
		}

		// 2. Check for valid results or explicit stop tokens (flexible "no issues" signals)
		isStopped := false
		for _, token := range b.StopTokens {
			if strings.Contains(respMsg.Content, token) {
				isStopped = true
				break
			}
		}

		if len(results) > 0 || isStopped || strings.Contains(respMsg.Content, "No significant insights found") {
			_ = b.ChatManager.AppendMessage(ctx, sessionID, newUserMsg)
			_ = b.ChatManager.AppendMessage(ctx, sessionID, respMsg)
			success = true
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

	b.RedisClient.Incr(ctx, "system:metrics:model:"+model+":absolute_failures")
	lastError = fmt.Errorf("max retries reached or cognitive failure: %v", lastError)
	return nil, lastError
}

// ValidateMarkdown performs a basic structural check on the markdown response.
// It returns a list of issues found.
func (b *BaseAgent) ValidateMarkdown(input string) []string {
	var issues []string

	// 1. Check for unclosed code blocks
	codeBlockCount := strings.Count(input, "```")
	if codeBlockCount%2 != 0 {
		issues = append(issues, "Unclosed markdown code block (missing closing ```).")
	}

	// 2. Check for empty code blocks
	reCodeBlock := regexp.MustCompile("(?s)```(?:[a-zA-Z0-9]*\\n)?(.*?)\\n?```")
	matches := reCodeBlock.FindAllStringSubmatch(input, -1)
	for _, match := range matches {
		if len(match) > 1 {
			content := strings.TrimSpace(match[1])
			if content == "" {
				issues = append(issues, "Empty markdown code block found (must contain real content).")
				break // Only need one report for this
			}
		}
	}

	// 3. Check for mismatched headers (e.g. # Header with no space)
	reHeader := regexp.MustCompile(`(?m)^#+[^\s#]`)
	if reHeader.MatchString(input) {
		issues = append(issues, "Invalid header format (missing space after # symbols).")
	}

	// 3. Check for empty links/images
	if strings.Contains(input, "[]()") || strings.Contains(input, "![]()") {
		issues = append(issues, "Empty markdown link or image reference found.")
	}

	// 4. Check for unclosed bold/italic
	boldCount := strings.Count(input, "**")
	if boldCount%2 != 0 {
		issues = append(issues, "Mismatched bold markers (missing closing **).")
	}

	return issues
}

// ParseAnalysisResults handles multi-report responses with an optional limit.
func (b *BaseAgent) ParseAnalysisResults(response string, limit int) []AnalysisResult {
	// Clean markdown code blocks if the model wrapped its entire response
	cleanResponse := strings.TrimSpace(response)
	if strings.HasPrefix(cleanResponse, "```") {
		// Find first newline
		if firstNewline := strings.Index(cleanResponse, "\n"); firstNewline != -1 {
			cleanResponse = cleanResponse[firstNewline+1:]
		}
		// Remove trailing ```
		cleanResponse = strings.TrimSuffix(strings.TrimSpace(cleanResponse), "```")
	}

	for _, token := range b.StopTokens {
		if strings.Contains(cleanResponse, token) {
			return nil
		}
	}
	if strings.Contains(cleanResponse, "No significant insights found") {
		return nil
	}
	var results []AnalysisResult
	re := regexp.MustCompile(`(?m)^\s*---\s*$`)
	for _, section := range re.Split(cleanResponse, -1) {
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
				// Clean the markdown bullet point from the start of the string
				cleaned := trimmed
				if strings.HasPrefix(trimmed, "-") || strings.HasPrefix(trimmed, "*") {
					cleaned = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(trimmed, "-"), "*"))
				} else {
					// Handle numbered lists like "1. ..."
					dotIdx := strings.Index(trimmed, ".")
					if dotIdx > 0 && dotIdx < 5 {
						cleaned = strings.TrimSpace(trimmed[dotIdx+1:])
					}
				}
				pathLines = append(pathLines, cleaned)
			}
		}
	}

	res.Summary = strings.TrimSpace(strings.Join(summaryLines, " "))
	res.Content = strings.TrimSpace(strings.Join(contentLines, "\n"))
	res.ImplementationPath = pathLines

	// Default Body logic (will be refined by specific agent tiers)
	res.Body = res.Content
	if res.Body == "" {
		res.Body = res.Summary
	}

	return res
}
