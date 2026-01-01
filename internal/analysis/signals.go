package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/EasterCompany/dex-event-service/internal/ollama"
)

// MessageSignals contains the analytical breakdown of a single user message.
type MessageSignals struct {
	Sentiment      float64  `json:"sentiment"`       // -1.0 to 1.0
	Intent         string   `json:"intent"`          // question, statement, command, banter
	TechnicalDepth int      `json:"technical_depth"` // 1-10
	Verbosity      int      `json:"verbosity"`       // 1-10
	Topics         []string `json:"topics"`
	Urgency        int      `json:"urgency"` // 1-10
	Mood           string   `json:"mood"`    // curious, frustrated, helpful, chaotic, etc.
}

// Extract uses an LLM to analyze message content and return structured signals.
func Extract(ctx context.Context, client *ollama.Client, model string, content string, history string) (*MessageSignals, string, error) {
	prompt := fmt.Sprintf(`Analyze the following user message within the provided context.
Provide a granular analytical breakdown in JSON format.

Context:
%s

User Message:
"%s"

Output EXACTLY this JSON structure:
{
  "sentiment": float, // -1.0 (very negative) to 1.0 (very positive)
  "intent": string, // "question", "statement", "command", "banter"
  "technical_depth": int, // 1-10
  "verbosity": int, // 1-10
  "topics": ["topic1", "topic2"],
  "urgency": int, // 1-10
  "mood": string // descriptive word
}`, history, content)

	raw, _, err := client.Generate(model, prompt, nil)
	if err != nil {
		return nil, "", err
	}

	// Clean up JSON if model wrapped it in markdown
	cleanJSON := raw
	if strings.Contains(raw, "```json") {
		parts := strings.Split(raw, "```json")
		if len(parts) > 1 {
			cleanJSON = strings.Split(parts[1], "```")[0]
		}
	} else if strings.Contains(raw, "```") {
		parts := strings.Split(raw, "```")
		if len(parts) > 1 {
			cleanJSON = parts[1]
		}
	}

	var signals MessageSignals
	if err := json.Unmarshal([]byte(strings.TrimSpace(cleanJSON)), &signals); err != nil {
		return nil, raw, fmt.Errorf("failed to parse signals JSON: %w", err)
	}

	return &signals, raw, nil
}
