package middleware

import (
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/EasterCompany/dex-event-service/config"
)

// ServiceAuthMiddleware validates that requests come from known services
func ServiceAuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Get client IP address first to check for localhost
		clientIP := getClientIP(r)
		if clientIP == "" {
			log.Printf("AUTH DENIED: Could not determine client IP from %s", r.RemoteAddr)
			http.Error(w, "Forbidden: Could not determine client IP", http.StatusForbidden)
			return
		}

		// Allow requests from localhost without authentication
		if isLocalhost(clientIP) {
			// log.Printf("AUTH BYPASS: Localhost request from %s", clientIP)
			next(w, r)
			return
		}

		// Extract service name from header
		serviceName := r.Header.Get("X-Service-Name")
		if serviceName == "" {
			log.Printf("AUTH DENIED: Missing X-Service-Name header from %s", r.RemoteAddr)
			http.Error(w, "Forbidden: X-Service-Name header required", http.StatusForbidden)
			return
		}

		// Load service map
		serviceMap, err := config.LoadServiceMap()
		if err != nil {
			log.Printf("AUTH ERROR: Failed to load service map: %v", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		// Verify service exists and IP matches
		if !validateServiceAccess(serviceName, clientIP, serviceMap) {
			log.Printf("AUTH DENIED: Service '%s' from IP '%s' not authorized (RemoteAddr: %s)",
				serviceName, clientIP, r.RemoteAddr)
			http.Error(w, "Forbidden: Service not authorized", http.StatusForbidden)
			return
		}

		log.Printf("AUTH OK: Service '%s' from IP '%s'", serviceName, clientIP)

		// Request is authorized, proceed
		next(w, r)
	}
}

// getClientIP extracts the real client IP from the request
func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header (if behind proxy)
	forwarded := r.Header.Get("X-Forwarded-For")
	if forwarded != "" {
		// X-Forwarded-For can be a comma-separated list, take the first one
		ips := strings.Split(forwarded, ",")
		if len(ips) > 0 {
			return strings.TrimSpace(ips[0])
		}
	}

	// Check X-Real-IP header (if behind nginx)
	realIP := r.Header.Get("X-Real-IP")
	if realIP != "" {
		return realIP
	}

	// Fall back to RemoteAddr
	// RemoteAddr is in format "IP:port", we need just the IP
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// If SplitHostPort fails, RemoteAddr might already be just an IP
		return r.RemoteAddr
	}

	return host
}

// validateServiceAccess checks if the service name exists in the service map
// and if the client IP matches the service's configured address
func validateServiceAccess(serviceName, clientIP string, serviceMap *config.ServiceMapConfig) bool {
	// Search for the service in all categories
	for _, services := range serviceMap.Services {
		for _, service := range services {
			if service.ID == serviceName {
				// Service found, now verify IP
				return matchesServiceIP(clientIP, service.Domain)
			}
		}
	}

	// Service not found in map
	return false
}

// matchesServiceIP checks if the client IP matches the service's configured address
func matchesServiceIP(clientIP, serviceAddr string) bool {
	// Normalize IPs for comparison
	clientIP = normalizeIP(clientIP)
	serviceAddr = normalizeIP(serviceAddr)

	// Special case: 0.0.0.0 means the service accepts connections from anywhere on localhost
	if serviceAddr == "0.0.0.0" {
		localhostIPs := []string{"127.0.0.1", "::1", "localhost"}
		if contains(localhostIPs, clientIP) {
			return true
		}
	}

	// Direct match
	if clientIP == serviceAddr {
		return true
	}

	// Special case: localhost variations
	localhostIPs := []string{"127.0.0.1", "::1", "localhost"}

	clientIsLocalhost := contains(localhostIPs, clientIP)
	serviceIsLocalhost := contains(localhostIPs, serviceAddr)

	if clientIsLocalhost && serviceIsLocalhost {
		return true
	}

	return false
}

// normalizeIP normalizes IP addresses for comparison
func normalizeIP(ip string) string {
	ip = strings.TrimSpace(ip)

	// Convert localhost to 127.0.0.1
	if ip == "localhost" {
		return "127.0.0.1"
	}

	// Convert IPv6 localhost to standard form
	if ip == "::1" || ip == "[::1]" {
		return "::1"
	}

	// Remove brackets from IPv6 addresses
	ip = strings.Trim(ip, "[]")

	return ip
}

// contains checks if a string slice contains a value
func contains(slice []string, val string) bool {
	for _, item := range slice {
		if item == val {
			return true
		}
	}
	return false
}

// isLocalhost checks if an IP address is localhost
func isLocalhost(ip string) bool {
	normalized := normalizeIP(ip)
	localhostIPs := []string{"127.0.0.1", "::1", "0.0.0.0", "localhost"}
	return contains(localhostIPs, normalized)
}
