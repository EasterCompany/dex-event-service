package endpoints

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/EasterCompany/dex-event-service/utils"
)

// WebHistoryHandler returns the last web view requests
func WebHistoryHandler(w http.ResponseWriter, r *http.Request) {
	if redisClient == nil {
		http.Error(w, "Redis client not initialized", http.StatusServiceUnavailable)
		return
	}

	history, err := utils.GetWebHistory(redisClient)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get web history: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(history); err != nil {
		http.Error(w, fmt.Sprintf("Failed to encode response: %v", err), http.StatusInternalServerError)
	}
}
