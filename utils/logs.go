package utils

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// LogError prints an error message to stderr.
func LogError(format string, v ...interface{}) {
	// A simple implementation for now. In a real service, you'd use a structured logger.
	fmt.Fprintf(os.Stderr, "ERROR: "+format+"\n", v...)
}

// GetSystemdLogs fetches the most recent logs for a given systemd service.
func GetSystemdLogs(serviceName string, lineCount int) ([]string, error) {
	// journalctl --user -u [serviceName].service -n [lineCount] --no-pager
	cmd := exec.Command("journalctl", "--user", "-u", serviceName+".service", "-n", string(rune(lineCount)), "--no-pager")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	var logs []string
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		// We can trim "-- Logs begin at..." and "-- Reboot --" if we want, but for now, let's keep it simple.
		line := scanner.Text()
		if !strings.HasPrefix(line, "--") { // Filter out journalctl metadata lines
			logs = append(logs, line)
		}
	}

	if err := cmd.Wait(); err != nil {
		// This could be a real error or just that the service doesn't exist.
		// We'll return the error to be handled by the caller.
		return nil, err
	}

	return logs, nil
}
