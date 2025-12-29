package agent

import (
	"context"
	"github.com/EasterCompany/dex-event-service/internal/ollama"
)

// AnalysisResult represents a standardized report from any agent.
type AnalysisResult struct {
	Type               string   `json:"type"` // alert, blueprint, etc.
	Title              string   `json:"title"`
	Priority           string   `json:"priority"`
	Category           string   `json:"category"`
	Body               string   `json:"body"`
	Summary            string   `json:"summary"`
	Content            string   `json:"content"`
	AffectedServices   []string `json:"affected_services"`
	ImplementationPath []string `json:"implementation_path"`
	RelatedEventIDs    []string `json:"related_event_ids"`
	AuditEventID       string   `json:"audit_event_id,omitempty"`
}

// AgentConfig holds parameters for the agent's behavior.
type AgentConfig struct {
	Name            string
	ProcessID       string
	Models          map[string]string // e.g. "t1": "dex-guardian-t1"
	Cooldowns       map[string]int    // e.g. "t1": 1800
	IdleRequirement int
}

// Agent is the interface all automated workflows must implement.
type Agent interface {
	Init(ctx context.Context) error
	Close() error
	Run(ctx context.Context) ([]AnalysisResult, error)
	GetConfig() AgentConfig
}

// CognitiveModule defines the shared logic for chat loops and parsing.
type CognitiveModule interface {
	ChatWithRetry(ctx context.Context, model string, history []ollama.Message, prompt string) ([]AnalysisResult, error)
}
