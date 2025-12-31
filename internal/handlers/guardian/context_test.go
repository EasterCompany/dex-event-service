package guardian

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

	// Check for double blank lines (triple newlines)
	if strings.Contains(t1Context, "\n\n\n") {
		t.Errorf("Tier 1 context contains double blank lines (triple newlines):\n%q", t1Context)
	}

	// Verify specific structure for T1
	expectedParts := []string{
		"### SYSTEM STATUS\n" + status,
		"### CLI CAPABILITIES\n" + cliHelp,
		"### RECENT LOGS\n" + logs,
		"### TEST RESULTS\n" + tests,
		"### HARDWARE & CONTEXT\n" + systemInfo,
		"### RECENT EVENTS:\n" + events,
	}
	for _, part := range expectedParts {
		if !strings.Contains(t1Context, part) {
			t.Errorf("Tier 1 context missing expected part: %q", part)
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

	// Verify T2 specific
	if !strings.Contains(t2Context, "### RECENT TIER 1 REPORTS:\n") {
		t.Error("Tier 2 context missing Tier 1 reports header")
	}
}
