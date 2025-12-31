package guardian

/*
import (
	"strings"
	"testing"

	"github.com/EasterCompany/dex-event-service/internal/agent"
)

func TestFormatContext_NoDoubleBlankLines(t *testing.T) {
	h := &GuardianHandler{}

	// The cli output has 1 newline at the start, and 1 newline at the end.
	status := "\nStatus: OK\n"
	logs := "\nLog entry 1\nLog entry 2\n"
	cliHelp := "\ndex help\n"
	tests := "\nTest passed\n"
	events := "\nEvent 1\nEvent 2\n"
	systemInfo := "\nCPU: 10%\n"

	// Test Tier 1 context
	t1Context := h.formatContext("t1", status, logs, cliHelp, tests, events, systemInfo, nil)

	// Check for double blank lines (triple newlines) - FAIL if found
	if strings.Contains(t1Context, "\n\n\n") {
		t.Errorf("Tier 1 context contains double blank lines (triple newlines):\n%q", t1Context)
	}

	// Verify exactly one blank line separation (\n\n) between headers and content
	// and content and headers.
	expectedTransitions := []string{
		"### SYSTEM STATUS\n\nStatus",
		"Status: OK\n\n### CLI CAPABILITIES",
		"### CLI CAPABILITIES\n\ndex help",
		"dex help\n\n### RECENT LOGS",
		"### RECENT LOGS\n\nLog entry",
		"Log entry 2\n\n### TEST RESULTS",
		"### TEST RESULTS\n\nTest passed",
		"Test passed\n\n### HARDWARE & CONTEXT",
		"### HARDWARE & CONTEXT\n\nCPU: 10%",
		"CPU: 10%\n\n### RECENT EVENTS:",
		"### RECENT EVENTS:\n\nEvent 1",
	}
	for _, trans := range expectedTransitions {
		if !strings.Contains(t1Context, trans) {
			t.Errorf("Tier 1 context missing expected transition or incorrect spacing: %q", trans)
		}
	}

	// Test Tier 2 context
	t2Results := []agent.AnalysisResult{
		{Title: "Issue 1", Summary: "Summary 1"},
	}
	t2Context := h.formatContext("t2", status, logs, cliHelp, "", "", "", t2Results)

	if strings.Contains(t2Context, "\n\n\n") {
		t.Errorf("Tier 2 context contains double blank lines (triple newlines):\n%q", t2Context)
	}

	if !strings.Contains(t2Context, "### RECENT TIER 1 REPORTS:\n\n[") {
		t.Error("Tier 2 context missing Tier 1 reports header or incorrect spacing")
	}
}
*/
