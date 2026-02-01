package middleware

import (
	"github.com/EasterCompany/dex-go-utils/middleware"
	"net/http"
)

// ServiceAuthMiddleware validates that requests come from known services.
// It now uses the shared implementation from dex-go-utils.
func ServiceAuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return middleware.ServiceAuthMiddleware(next)
}
