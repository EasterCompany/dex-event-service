package endpoints

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/EasterCompany/dex-event-service/utils"
)

// WebHistoryHandler returns the last web view requests or stores a new one
func WebHistoryHandler(w http.ResponseWriter, r *http.Request) {
	if redisClient == nil {
		http.Error(w, "Redis client not initialized", http.StatusServiceUnavailable)
		return
	}

	if r.Method == http.MethodGet {
		history, err := utils.GetWebHistory(redisClient)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to get web history: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(history); err != nil {
			http.Error(w, fmt.Sprintf("Failed to encode response: %v", err), http.StatusInternalServerError)
		}
		return
	}

	if r.Method == http.MethodPost {
		var item utils.WebHistoryItem
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		// Ensure timestamp is set
		if item.Timestamp == 0 {
			item.Timestamp = time.Now().Unix()
		}

		if err := utils.StoreWebHistoryItem(redisClient, item); err != nil {
			http.Error(w, fmt.Sprintf("Failed to store history: %v", err), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusCreated)
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}
