package endpoints

import (
	"fmt"
	"net/http"

	"github.com/EasterCompany/dex-event-service/utils"
)

// ServiceHandler provides a simple health check endpoint.
func ServiceHandler(w http.ResponseWriter, r *http.Request) {
	health := utils.GetHealth()
	if health.Status == "healthy" {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "OK")
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprintln(w, "BAD")
	}
}
