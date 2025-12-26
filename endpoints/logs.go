package endpoints

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/EasterCompany/dex-event-service/config"
	"github.com/EasterCompany/dex-event-service/types"
)

// LogsHandler collects logs for all configured services and returns as JSON
func LogsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		handleDeleteLogs(w, r)
		return
	}

	configuredServices, err := config.LoadServiceMap()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load service map: %v", err), http.StatusInternalServerError)
		return
	}

	var reports []types.LogReport

	// Get sorted group keys to ensure consistent order
	var groupKeys []string
	for group := range configuredServices.Services {
		groupKeys = append(groupKeys, group)
	}
	sort.Slice(groupKeys, func(i, j int) bool {
		return groupKeys[i] < groupKeys[j]
	})

	// Iterate through sorted service groups
	for _, group := range groupKeys {
		servicesInGroup := configuredServices.Services[group]

		// Sort services within each group by ID for consistent ordering
		sort.Slice(servicesInGroup, func(i, j int) bool {
			return servicesInGroup[i].ID < servicesInGroup[j].ID
		})

		for _, serviceDef := range servicesInGroup {
			// We are only interested in manageable services that have log files
			if !isServiceManageable(serviceDef.ID) {
				continue
			}
			report := getLogReport(serviceDef)
			reports = append(reports, report)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(reports); err != nil {
		http.Error(w, fmt.Sprintf("Failed to encode service reports: %v", err), http.StatusInternalServerError)
	}
}

func handleDeleteLogs(w http.ResponseWriter, r *http.Request) {
	serviceID := r.URL.Query().Get("service_id")
	if serviceID == "" {
		http.Error(w, "service_id parameter is required", http.StatusBadRequest)
		return
	}

	// Validate service ID against service map to prevent arbitrary file deletion
	// although isServiceManageable and getLogPath are simple, we should arguably check if it exists in config
	// For now, using isServiceManageable as a basic filter + getLogPath structure
	if !isServiceManageable(serviceID) {
		http.Error(w, "service is not manageable", http.StatusForbidden)
		return
	}

	logPath := getLogPath(serviceID)
	// Check if file exists before trying to truncate
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		http.Error(w, "Log file not found", http.StatusNotFound)
		return
	}

	if err := os.Truncate(logPath, 0); err != nil {
		http.Error(w, fmt.Sprintf("failed to clear log file: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Logs cleared successfully"))
}

func getLogReport(service config.ServiceEntry) types.LogReport {
	logPath := getLogPath(service.ID)
	logs, err := readLastNLines(logPath, 100)
	if err != nil {
		return types.LogReport{
			ID:   service.ID,
			Logs: []string{fmt.Sprintf("Error reading log file: %v", err)},
		}
	}

	return types.LogReport{
		ID:   service.ID,
		Logs: logs,
	}
}

func getLogPath(serviceID string) string {
	home := os.Getenv("HOME")
	return fmt.Sprintf("%s/Dexter/logs/%s.log", home, serviceID)
}

func isServiceManageable(serviceID string) bool {
	// This is a simplified check. In a real-world scenario, you would have a more robust way
	// to determine if a service is manageable.
	return !strings.Contains(serviceID, "cli") && !strings.Contains(serviceID, "os")
}

func readLastNLines(filePath string, n int) ([]string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := file.Close(); err != nil {
			// Log the error, but don't return it as the primary function result
			fmt.Fprintf(os.Stderr, "Error closing log file %s: %v\n", filePath, err)
		}
	}()

	var lines []string
	scanner := bufio.NewScanner(file)
	// Create a larger buffer to handle long log lines (e.g., stack traces)
	// 10MB max token size should be sufficient
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}

	return lines, scanner.Err()
}
