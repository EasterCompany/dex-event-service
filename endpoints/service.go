package endpoints

import (
	"encoding/json"
	"net/http"

	"github.com/EasterCompany/dex-event-service/utils"
)

// ServiceHandler provides the UNIVERSAL service status endpoint.
// This endpoint MUST return the standard structure used by ALL Dexter services.
func ServiceHandler(w http.ResponseWriter, r *http.Request) {
	// Support ?format=version for simple version string
	if r.URL.Query().Get("format") == "version" {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(utils.GetVersion().Str))
		return
	}

	// Build standard service report with system metrics
	systemMetrics := utils.GetMetrics()
	metrics := map[string]interface{}{
		"cpu":    systemMetrics.CPU,
		"memory": systemMetrics.Memory,
	}

	report := utils.ServiceReport{
		Version: utils.GetVersion(),
		Health:  utils.GetHealth(),
		Metrics: metrics,
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
