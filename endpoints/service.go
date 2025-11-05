package endpoints

import (
	"encoding/json"
	"net/http"

	"github.com/EasterCompany/dex-event-service/utils"
)

// ServiceHandler returns a comprehensive status report for the service.
func ServiceHandler(w http.ResponseWriter, r *http.Request) {
	logs, err := utils.GetSystemdLogs("dex-event-service", 20)
	if err != nil {
		// If fetching logs fails, we can report the error in the logs field.
		logs = []string{"Failed to fetch systemd logs: " + err.Error()}
	}

	report := utils.ServiceReport{
		Version: utils.GetVersion(),
		Status:  "OK",
		Health:  utils.GetHealth(),
		Logs:    logs,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(report); err != nil {
		// In a real app, you'd have structured logging here
		http.Error(w, "Failed to encode service report", http.StatusInternalServerError)
	}
}
