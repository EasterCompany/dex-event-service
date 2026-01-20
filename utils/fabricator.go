package utils

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// CheckFabricatorPro checks if the Fabricator Pro model has available quota.
// Returns (isAvailable, resetTime, error)
func CheckFabricatorPro() (bool, string, error) {
	// We run 'dex-fabricator-cli info' to see if we get a quota error.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	binPath, err := exec.LookPath("dex-fabricator-cli")
	if err != nil {
		return false, "", fmt.Errorf("dex-fabricator-cli not found in PATH")
	}

	cmd := exec.CommandContext(ctx, binPath, "info")
	var out bytes.Buffer
	cmd.Stderr = &out
	cmd.Stdout = &out
	_ = cmd.Run() // We ignore exit code because 'info' might fail if quota is hit

	output := out.String()
	if strings.Contains(output, "TerminalQuotaError") || strings.Contains(output, "exhausted your capacity") {
		// Extract reset time
		re := regexp.MustCompile(`reset after ([\w\s]+)\.`)
		matches := re.FindStringSubmatch(output)
		if len(matches) > 1 {
			return false, matches[1], nil
		}
		return false, "unknown time", nil
	}

	// Also check for general lack of authenticated session or other errors
	if strings.Contains(output, "Login required") || strings.Contains(output, "Session not found") {
		return false, "login required", nil
	}

	return true, "", nil
}
