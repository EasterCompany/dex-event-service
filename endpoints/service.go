package endpoints

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/EasterCompany/dex-event-service/config"
	"github.com/EasterCompany/dex-event-service/utils"
)

// ServiceHandler provides a comprehensive status report for the service.
func ServiceHandler(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadServiceMap()
	if err != nil {
		// If config fails to load, we can't be sure of the service's state.
		http.Error(w, "Failed to load service configuration", http.StatusInternalServerError)
		return
	}

	systemCfg, err := config.LoadSystem()
	if err != nil {
		http.Error(w, "Failed to load system configuration", http.StatusInternalServerError)
		return
	}

	// Omit sensitive fields from the config report
	safeConfig := make(map[string]interface{})
	for key, val := range cfg.GetSanitized() {
		safeConfig[key] = val
	}

	logs, err := utils.GetSystemdLogs(systemCfg.Packages[0].Name, 10)
	if err != nil {
		// If logs can't be fetched, we can still return a report.
		// We'll add a note to the logs field about the error.
		logs = []string{fmt.Sprintf("Failed to fetch systemd logs: %v", err)}
	}

	// Get the full version information.
	version := utils.GetVersion()

	// Create a display version with a shortened string for the report.
	displayVersion := utils.Version{
		Str: fmt.Sprintf("%s.%s.%s", version.Obj.Major, version.Obj.Minor, version.Obj.Patch),
		Obj: version.Obj,
	}

	report := utils.ServiceReport{
		Version: displayVersion,
		Health:  utils.GetHealth(),
		Metrics: utils.Metrics{}, // Placeholder for future implementation
		Logs:    logs,
		Config:  safeConfig,
	}

	w.Header().Set("Content-Type", "application/json")

	// Check health status to set the correct HTTP status code
	if report.Health.Status == "OK" {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	// Encode the full report as JSON
	if err := json.NewEncoder(w).Encode(report); err != nil {
		// This is an internal error, the response is likely already partially sent.
		// We can't send a new http.Error, but we can log it.
		utils.LogError("Failed to encode service report: %v", err)
	}
}
