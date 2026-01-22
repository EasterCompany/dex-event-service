package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/EasterCompany/dex-event-service/internal/model"
	"github.com/EasterCompany/dex-event-service/utils"
	"github.com/redis/go-redis/v9"
)

// BaseAgent provides shared utilities for all agents.
type BaseAgent struct {
	RedisClient *redis.Client
	ModelClient *model.Client
	ChatManager *utils.ChatContextManager
	StopTokens  []string
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

// ValidateJSON ensures the output is valid JSON and matches the optional schema.
func (b *BaseAgent) ValidateJSON(input string, schema interface{}) []Correction {
	var corrections []Correction

	// 1. Syntax Check
	cleanJSON := b.CleanJSON(input)
	if !json.Valid([]byte(cleanJSON)) {
		corrections = append(corrections, Correction{
			Type: "SYNTAX", Guidance: "Invalid JSON syntax. Ensure you are not including trailing commas, unescaped quotes, or surrounding text outside the JSON block.", Mandatory: true,
		})
		return corrections
	}

	// 2. Schema Check (if schema provided)
	if schema != nil {
		// Create a new instance of the schema type
		schemaType := fmt.Sprintf("%T", schema)

		// Note: This is a shallow check. Real reflection-based deep validation
		// could be added here, but for now we just check if it unmarshals into the same type.
		var target interface{}

		// If it's a map or slice, we can't easily "copy" the type without reflection,
		// but we can try to unmarshal into a generic map/slice first to catch broad errors.
		err := json.Unmarshal([]byte(cleanJSON), &target)
		if err != nil {
			corrections = append(corrections, Correction{
				Type: "SCHEMA", Guidance: fmt.Sprintf("JSON structure mismatch. Expected structure type: %s. Error: %v", schemaType, err), Mandatory: true,
			})
		}
	}

	return corrections
}

// CleanJSON extracts JSON from markdown blocks if present.
func (b *BaseAgent) CleanJSON(s string) string {
	clean := strings.TrimSpace(s)
	if strings.HasPrefix(clean, "```json") {
		parts := strings.Split(clean, "```json")
		if len(parts) > 1 {
			return strings.Split(parts[1], "```")[0]
		}
	}
	if strings.HasPrefix(clean, "```") {
		parts := strings.Split(clean, "```")
		if len(parts) > 1 {
			return parts[1]
		}
	}
	return clean
}

// RunCognitiveLoop executes the 3-attempt chat process with retry logic.
// It ALWAYS produces a system.analysis.audit event regardless of outcome.
func (b *BaseAgent) RunCognitiveLoop(ctx context.Context, agent Agent, tierName, modelName, sessionID, systemPrompt, inputContext string, limit int) (results []AnalysisResult, auditEventID string, lastError error) {
	startTime := time.Now()
	agentConfig := agent.GetConfig()

	// Inject current date/time if aware
	if agentConfig.DateTimeAware {
		now := time.Now().Format("Monday, 02 Jan 2006 15:04:05 MST")
		inputContext = fmt.Sprintf("## CURRENT DATE/TIME\n\n%s\n\n%s", now, inputContext)
	}

	// Prepend Task Description
	protocolAlias := tierName
	if alias, ok := agentConfig.ProtocolAliases[tierName]; ok {
		protocolAlias = alias
	}
	taskHeader := fmt.Sprintf("# TASK\n\nYour task is to generate a %s %s report from the following data.\n\n", agentConfig.Name, protocolAlias)
	inputContext = taskHeader + inputContext

	var allCorrections []Correction
	var rawOutput string
	var currentTurnHistory []model.Message
	attempts := 0
	success := false

	defer func() {
		duration := time.Since(startTime).String()
		errStr := ""
		if lastError != nil {
			errStr = lastError.Error()
		}

		status := "SUCCESS"
		if !success {
			status = "FAIL"
		}

		audit := AuditPayload{
			Type:          "system.analysis.audit",
			AgentName:     agentConfig.Name,
			Tier:          protocolAlias,
			Model:         modelName,
			InputContext:  inputContext,
			RawOutput:     rawOutput,
			ParsedResults: results,
			Corrections:   allCorrections,
			ChatHistory:   currentTurnHistory,
			Error:         errStr,
			Duration:      duration,
			Timestamp:     time.Now().Unix(),
			Attempts:      attempts,
			Success:       success,
			Status:        status,
		}

		// Convert to map for SendEvent
		var auditMap map[string]interface{}
		auditJSON, _ := json.Marshal(audit)
		_ = json.Unmarshal(auditJSON, &auditMap)

		auditEventID, _ = utils.SendEvent(ctx, b.RedisClient, "dex-event-service", "system.analysis.audit", auditMap)
	}()

	chatHistory, _ := b.ChatManager.LoadHistory(ctx, sessionID)
	if systemPrompt != "" {
		if len(chatHistory) == 0 || chatHistory[0].Role != "system" {
			chatHistory = append([]model.Message{{Role: "system", Content: systemPrompt}}, chatHistory...)
		} else {
			chatHistory[0].Content = systemPrompt
		}
	}

	newUserMsg := model.Message{Role: "user", Content: inputContext}
	currentTurnHistory = append(chatHistory, newUserMsg)

	maxRetries := 5

	for i := 0; i < maxRetries; i++ {
		attempts++
		b.RedisClient.Incr(ctx, "system:metrics:model:"+modelName+":attempts")

		tCtx, cancel := context.WithTimeout(ctx, 60*time.Minute)
		respMsg, err := b.ModelClient.Chat(tCtx, modelName, currentTurnHistory)
		cancel()

		if err != nil {
			lastError = err
			b.RedisClient.Incr(ctx, "system:metrics:model:"+modelName+":failures")
			continue
		}

		rawOutput = respMsg.Content
		currentTurnHistory = append(currentTurnHistory, respMsg)
		var currentAttemptCorrections []Correction

		// --- Tier 1: Syntax Validation ---
		if agentConfig.EnforceMarkdown {
			mdIssues := b.ValidateMarkdown(respMsg.Content)
			if len(mdIssues) > 0 {
				currentAttemptCorrections = append(currentAttemptCorrections, mdIssues...)
			}

			// Integrity Check (Mutually Exclusive Content)
			integrityIssues := b.ValidateResponseIntegrity(respMsg.Content)
			if len(integrityIssues) > 0 {
				currentAttemptCorrections = append(currentAttemptCorrections, integrityIssues...)
			}
		}

		// --- Tier 1.5: JSON Validation ---
		if agentConfig.EnforceJSON {
			jsonIssues := b.ValidateJSON(respMsg.Content, agentConfig.JSONSchema)
			if len(jsonIssues) > 0 {
				currentAttemptCorrections = append(currentAttemptCorrections, jsonIssues...)
			}
		}

		// --- Tier 2: Schema Validation (Only if syntax passes) ---
		if len(currentAttemptCorrections) == 0 {
			if agentConfig.EnforceJSON {
				// Bypass markdown parsing for JSON agents
				results = []AnalysisResult{{
					Type:    "json_output",
					Title:   "JSON Output",
					Content: respMsg.Content,
				}}
			} else {
				results = b.ParseAnalysisResults(respMsg.Content, limit)

				// Check for required sections if it's not a stop token
				isStopped := false
				for _, token := range b.StopTokens {
					if strings.Contains(respMsg.Content, token) {
						isStopped = true
						break
					}
				}

				if !isStopped && len(results) > 0 {
					for _, res := range results {
						schemaIssues := b.ValidateSchema(res, agentConfig.RequiredSections)
						currentAttemptCorrections = append(currentAttemptCorrections, schemaIssues...)

						// --- Tier 3: Logic Validation (Protocol Specific) ---
						if len(schemaIssues) == 0 {
							logicIssues := agent.ValidateLogic(res)
							currentAttemptCorrections = append(currentAttemptCorrections, logicIssues...)
						}
					}
				} else if !isStopped && !strings.Contains(respMsg.Content, "No significant insights found") {
					// No results parsed but no stop token found either
					currentAttemptCorrections = append(currentAttemptCorrections, Correction{
						Type: "SCHEMA", Guidance: "Your response did not contain any valid Dexter Reports (# Title). Ensure you follow the strict report format.", Mandatory: true,
					})
				}
			}
		}

		// Handle Rejection or Success
		if len(currentAttemptCorrections) > 0 {
			allCorrections = append(allCorrections, currentAttemptCorrections...)
			b.RedisClient.Incr(ctx, "system:metrics:model:"+modelName+":failures")

			feedback := b.BuildFeedbackPrompt(currentAttemptCorrections, agentConfig)
			feedbackMsg := model.Message{
				Role:    "user",
				Content: feedback,
			}
			currentTurnHistory = append(currentTurnHistory, feedbackMsg)
			continue
		}

		// If we reached here, validation passed or it was an explicit stop
		_ = b.ChatManager.AppendMessage(ctx, sessionID, newUserMsg)
		_ = b.ChatManager.AppendMessage(ctx, sessionID, respMsg)
		success = true
		return
	}

	b.RedisClient.Incr(ctx, "system:metrics:model:"+modelName+":absolute_failures")
	lastError = fmt.Errorf("max retries reached or cognitive failure: %v", lastError)
	return
}

// BuildFeedbackPrompt constructs a high-fidelity rejection report for the model.
func (b *BaseAgent) BuildFeedbackPrompt(corrections []Correction, config AgentConfig) string {
	var sb strings.Builder
	sb.WriteString("# REPORT REJECTED\n")
	sb.WriteString("**Reason:** Your previous response contained structural or logical errors. You MUST fix these issues in your next attempt.\n\n")

	// Group by type
	byType := make(map[string][]Correction)
	for _, c := range corrections {
		byType[c.Type] = append(byType[c.Type], c)
	}

	types := []string{"SYNTAX", "SCHEMA", "LOGIC"}
	for _, t := range types {
		corrs := byType[t]
		if len(corrs) == 0 {
			continue
		}

		sb.WriteString(fmt.Sprintf("## %s ERRORS\n", t))
		for _, c := range corrs {
			if c.Line > 0 {
				sb.WriteString(fmt.Sprintf("(Line %d) ", c.Line))
			}
			if c.Snippet != "" {
				sb.WriteString(fmt.Sprintf("> **Violation:** `%s`\n", c.Snippet))
			}
			sb.WriteString(fmt.Sprintf("> **Guidance:** %s\n\n", c.Guidance))
		}
	}

	// Dynamic Template Generation
	if len(config.RequiredSections) > 0 {
		sb.WriteString("### REFERENCE: EXPECTED FORMAT STRUCTURE\n")
		sb.WriteString("Ensure your response strictly follows this markdown skeleton:\n\n")
		sb.WriteString("```markdown\n")
		sb.WriteString("# [Report Title]\n\n")

		// 1. Metadata Fields
		for _, section := range config.RequiredSections {
			lower := strings.ToLower(section)
			if lower == "priority" || lower == "category" || lower == "related" || lower == "related services" {
				sb.WriteString(fmt.Sprintf("**%s**: [Value]\n", section))
			}
		}
		sb.WriteString("\n")

		// 2. Content Sections
		for _, section := range config.RequiredSections {
			lower := strings.ToLower(section)
			if lower != "priority" && lower != "category" && lower != "related" && lower != "related services" {
				sb.WriteString(fmt.Sprintf("## %s\n(Your content here...)\n\n", section))
			}
		}
		sb.WriteString("```\n\n")
	}

	sb.WriteString("Please resubmit your complete, corrected report now.")
	return sb.String()
}

// ValidateResponseIntegrity ensures the model doesn't mix "no action" tokens with actual content.
func (b *BaseAgent) ValidateResponseIntegrity(input string) []Correction {
	var corrections []Correction

	hasHeader := false
	lines := strings.Split(input, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# ") {
			hasHeader = true
			break
		}
	}

	for _, token := range b.StopTokens {
		if strings.Contains(input, token) && hasHeader {
			corrections = append(corrections, Correction{
				Type:      "LOGIC",
				Guidance:  fmt.Sprintf("Ambiguous response detected. You included both a Report Header (# Title) and a Stop Token ('%s'). You must choose ONE: either provide a full report OR output the stop token if no action is needed. Do not do both.", token),
				Mandatory: true,
			})
		}
	}

	return corrections
}

// ValidateMarkdown performs a basic structural check on the markdown response.
func (b *BaseAgent) ValidateMarkdown(input string) []Correction {
	var corrections []Correction
	lines := strings.Split(input, "\n")

	// 1. Check for unclosed code blocks (Fatal Syntax)
	codeBlockCount := strings.Count(input, "```")
	if codeBlockCount%2 != 0 {
		corrections = append(corrections, Correction{
			Type: "SYNTAX", Guidance: "Unclosed markdown code block. You opened a block with ``` but never closed it.", Mandatory: true,
		})
		return corrections
	}

	// 2. Line-by-line validation (Spatial Awareness)
	inCodeBlock := false
	codeBlockHasContent := false
	codeBlockStartLine := 0

	for i, line := range lines {
		lineNum := i + 1
		trimmed := strings.TrimSpace(line)

		// Code block tracking
		if strings.HasPrefix(trimmed, "```") {
			if !inCodeBlock {
				inCodeBlock = true
				codeBlockHasContent = false
				codeBlockStartLine = lineNum
			} else {
				// Closing a block
				if !codeBlockHasContent {
					corrections = append(corrections, Correction{
						Type: "SYNTAX", Line: codeBlockStartLine, Snippet: "```\n\n```",
						Guidance: "Empty code block found. Technical segments must contain real code, logs, or steps.", Mandatory: true,
					})
				}
				inCodeBlock = false
			}
			continue
		}

		if inCodeBlock {
			if trimmed != "" {
				codeBlockHasContent = true
			}
			continue
		}

		// Header validation (Only outside code blocks)
		if strings.HasPrefix(trimmed, "#") {
			reHeader := regexp.MustCompile(`^#+[^\s#]`)
			if reHeader.MatchString(trimmed) {
				corrections = append(corrections, Correction{
					Type: "SYNTAX", Line: lineNum, Snippet: trimmed,
					Guidance: "Invalid header format. There must be a space between the '#' symbols and the header text.", Mandatory: true,
				})
			}
		}

		// Empty references
		if strings.Contains(line, "[]()") || strings.Contains(line, "![]()") {
			corrections = append(corrections, Correction{
				Type: "SYNTAX", Line: lineNum, Snippet: "[]()",
				Guidance: "Empty markdown link or image reference found.", Mandatory: true,
			})
		}
	}

	return corrections
}

// ValidateSchema ensures all required sections are present in the result.
func (b *BaseAgent) ValidateSchema(res AnalysisResult, required []string) []Correction {
	var corrections []Correction

	for _, section := range required {
		missing := false
		switch strings.ToLower(section) {
		case "summary":
			if res.Summary == "" {
				missing = true
			}
		case "content", "body", "insight":
			if res.Content == "" {
				missing = true
			}
		case "priority":
			if res.Priority == "" {
				missing = true
			}
		case "category":
			if res.Category == "" {
				missing = true
			}
		case "related", "related services":
			if len(res.RelatedServices) == 0 {
				missing = true
			}
		case "implementation path", "proposed steps", "implementation plan", "implementation_steps":
			if len(res.ImplementationPath) == 0 {
				missing = true
			}
		}

		if missing {
			msg := fmt.Sprintf("Missing mandatory section: '%s'. You must include this section in your report.", section)
			lowerSect := strings.ToLower(section)
			if lowerSect == "priority" || lowerSect == "category" || lowerSect == "related" || lowerSect == "related services" {
				msg = fmt.Sprintf("Missing mandatory metadata field: '**%s**:'. You must include this field at the top of your report.", section)
			}

			corrections = append(corrections, Correction{
				Type: "SCHEMA", Guidance: msg, Mandatory: true,
			})
		}
	}

	// Basic quality check on sections
	if res.Title == "" || len(res.Title) < 5 {
		corrections = append(corrections, Correction{
			Type: "SCHEMA", Guidance: "Report title is missing or too short. Provide a descriptive title starting with #.", Mandatory: true,
		})
	}

	return corrections
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
		if strings.HasPrefix(lower, "**related**:") || strings.HasPrefix(lower, "**related services**:") {
			res.RelatedServices = strings.Split(strings.TrimSpace(trimmed[strings.Index(trimmed, ":")+1:]), ",")
			continue
		}

		// Section headers (Must start with #)
		if strings.HasPrefix(trimmed, "#") {
			if strings.Contains(lower, "summary") || strings.Contains(lower, "overview") {
				currentSection = "summary"
				continue
			}
			if strings.Contains(lower, "content") || strings.Contains(lower, "insight") || strings.Contains(lower, "body") || strings.Contains(lower, "analysis") {
				currentSection = "content"
				continue
			}
			if strings.Contains(lower, "implementation path") || strings.Contains(lower, "proposed steps") || strings.Contains(lower, "implementation plan") {
				currentSection = "path"
				continue
			}
			// Unknown header - stop capture
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
