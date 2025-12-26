package endpoints

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
)

// whitelistedCommands defines which dex commands are allowed to be executed via API.
var whitelistedCommands = map[string]bool{
	"guardian": true,
	"test":     true,
	"status":   true,
	"system":   true,
	"config":   true,
	"logs":     true,
	"build":    true,
	"update":   true,
}

func getDexBinaryPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "dex"
	}
	return filepath.Join(home, "Dexter", "bin", "dex")
}

// HandleCLIExecute runs a whitelisted dex command and streams the output back to the client.
func HandleCLIExecute(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if !whitelistedCommands[req.Command] {
		http.Error(w, fmt.Sprintf("Command '%s' is not whitelisted for remote execution", req.Command), http.StatusForbidden)
		return
	}

	// Prepare the command
	dexPath := getDexBinaryPath()
	cmdArgs := append([]string{req.Command}, req.Args...)

	// Add --json or other flags if necessary, but usually args are passed from frontend
	cmd := exec.Command(dexPath, cmdArgs...)

	// Set environment variables to ensure color output or specific behavior if needed
	cmd.Env = append(os.Environ(), "FORCE_COLOR=1", "DEX_INTERACTIVE=false")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to create stdout pipe: %v", err), http.StatusInternalServerError)
		return
	}
	cmd.Stderr = cmd.Stdout // Merge stderr into stdout

	if err := cmd.Start(); err != nil {
		http.Error(w, fmt.Sprintf("Failed to start command: %v", err), http.StatusInternalServerError)
		return
	}

	// Set headers for streaming
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// Stream the output
	scanner := bufio.NewScanner(stdout)
	// Larger buffer for long lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	flusher, ok := w.(http.Flusher)

	for scanner.Scan() {
		line := scanner.Text()
		_, _ = fmt.Fprintln(w, line)
		if ok {
			flusher.Flush()
		}
	}

	if err := cmd.Wait(); err != nil {
		_, _ = fmt.Fprintf(w, "\n[ERROR] Command exited with error: %v\n", err)
	} else {
		_, _ = fmt.Fprintf(w, "\n[SUCCESS] Command completed successfully.\n")
	}
}
