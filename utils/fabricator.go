package utils

import (
	"bytes"
	"os/exec"
	"regexp"
	"strings"
)

// CheckFabricatorPro checks if the Fabricator Pro model has available quota.
// Returns (isAvailable, resetTime, error)
func CheckFabricatorPro() (bool, string, error) {
	// We run 'fabricator info' to see if we get a quota error.
	// We capture both stdout and stderr as the error can go to either.
	binPath, err := exec.LookPath("fabricator")
	if err != nil {
		return false, "", err
	}

	cmd := exec.Command(binPath, "info")
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
