package endpoints

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gorilla/mux"
)

// SystemOptionsHandler handles reading and writing system options
func SystemOptionsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		handleGetSystemOptions(w, r)
	case http.MethodPost:
		handleSetSystemOptions(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// SystemServiceControlHandler handles start/stop/restart of services
func SystemServiceControlHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	action := vars["action"]

	var req struct {
		Service string `json:"service"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Service == "" {
		http.Error(w, "Service name required", http.StatusBadRequest)
		return
	}

	// Support "all" keyword to map to empty string for CLI (global action)
	targetService := req.Service
	if targetService == "all" {
		targetService = ""
	}

	validActions := map[string]bool{"start": true, "stop": true, "restart": true}
	if !validActions[action] {
		http.Error(w, "Invalid action", http.StatusBadRequest)
		return
	}

	home, err := os.UserHomeDir()
	if err != nil {
		http.Error(w, "Failed to get home dir", http.StatusInternalServerError)
		return
	}
	dexPath := filepath.Join(home, "Dexter", "bin", "dex")

	// Execute dex <action> [service]
	args := []string{action}
	if targetService != "" {
		args = append(args, targetService)
	}

	cmd := exec.Command(dexPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(output))
		// If msg is empty, use err
		if msg == "" {
			msg = err.Error()
		}
		http.Error(w, fmt.Sprintf("Command failed: %s", msg), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	targetDisplay := targetService
	if targetDisplay == "" {
		targetDisplay = "all services"
	}
	_, _ = fmt.Fprintf(w, "Service %s %sed", targetDisplay, action)
}

func handleGetSystemOptions(w http.ResponseWriter, r *http.Request) {
	// Read options.json directly
	home, err := os.UserHomeDir()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get home dir: %v", err), http.StatusInternalServerError)
		return
	}
	optionsPath := filepath.Join(home, "Dexter", "config", "options.json")

	data, err := os.ReadFile(optionsPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Return empty map if file doesn't exist
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"services": {}}`))
			return
		}
		http.Error(w, fmt.Sprintf("Failed to read options.json: %v", err), http.StatusInternalServerError)
		return
	}

	// We only want to return the "services" part for now, or maybe the whole thing?
	// The frontend specifically wants service configurations.
	// Let's parse and return just the Services map to be safe/focused.
	var opts struct {
		Services map[string]map[string]interface{} `json:"services"`
	}
	if err := json.Unmarshal(data, &opts); err != nil {
		http.Error(w, fmt.Sprintf("Failed to parse options.json: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if opts.Services == nil {
		_, _ = w.Write([]byte(`{}`))
	} else {
		_ = json.NewEncoder(w).Encode(opts.Services)
	}
}

func handleSetSystemOptions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Service string `json:"service"`
		Key     string `json:"key"`
		Value   string `json:"value"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Service == "" || req.Key == "" || req.Value == "" {
		http.Error(w, "Missing required fields (service, key, value)", http.StatusBadRequest)
		return
	}

	// Call dex config set
	home, err := os.UserHomeDir()
	if err != nil {
		http.Error(w, "Failed to get home dir", http.StatusInternalServerError)
		return
	}
	dexPath := filepath.Join(home, "Dexter", "bin", "dex")

	cmd := exec.Command(dexPath, "config", "set", req.Service, req.Key, req.Value)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Clean up error message
		msg := strings.TrimSpace(string(output))
		http.Error(w, fmt.Sprintf("Failed to set config: %s", msg), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Configuration updated"))
}
