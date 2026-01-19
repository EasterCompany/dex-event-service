package endpoints

import (
	"encoding/json"
	"net/http"

	"github.com/EasterCompany/dex-event-service/utils"
)

// DebugResolveModelHandler provides an endpoint to test the model resolution logic.
func DebugResolveModelHandler(w http.ResponseWriter, r *http.Request) {
	base := r.URL.Query().Get("base")
	device := r.URL.Query().Get("device")
	speed := r.URL.Query().Get("speed")

	if base == "" {
		http.Error(w, "missing 'base' parameter", http.StatusBadRequest)
		return
	}

	resolved := utils.ResolveModel(base, device, speed)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"base":     base,
		"device":   device,
		"speed":    speed,
		"resolved": resolved,
	})
}
