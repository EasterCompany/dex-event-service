package middleware

import (
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/EasterCompany/dex-go-utils/config"
)

// ServiceAuthMiddleware validates that requests come from known services
func ServiceAuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clientIP := getClientIP(r)
		if clientIP == "" {
			log.Printf("AUTH DENIED: Could not determine client IP from %s", r.RemoteAddr)
			http.Error(w, "Forbidden: Could not determine client IP", http.StatusForbidden)
			return
		}

		// Fast path: Localhost
		if isLocalhost(clientIP) {
			next(w, r)
			return
		}

		// Load service map to check if IP is a known service node
		serviceMap, err := config.LoadServiceMap()
		if err != nil {
			log.Printf("AUTH ERROR: Failed to load service map: %v", err)
			// Fallback to requiring X-Service-Name if map fails to load?
			// No, better to fail secure if the map is missing.
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		// NEW LOGIC: Trust ANY request from a known service node (Desktop or Cloud)
		if isKnownServiceIP(clientIP, serviceMap) {
			// log.Printf("AUTH OK: Trusted Service IP '%s'", clientIP)
			next(w, r)
			return
		}

		// Legacy/Strict Path: Require X-Service-Name for unknown IPs
		serviceName := r.Header.Get("X-Service-Name")
		if serviceName == "" {
			log.Printf("AUTH DENIED: Missing X-Service-Name header from %s", r.RemoteAddr)
			http.Error(w, "Forbidden: X-Service-Name header required for unknown node", http.StatusForbidden)
			return
		}

		if !validateServiceAccess(serviceName, clientIP, serviceMap) {
			log.Printf("AUTH DENIED: Service '%s' from IP '%s' not authorized (RemoteAddr: %s)",
				serviceName, clientIP, r.RemoteAddr)
			http.Error(w, "Forbidden: Service not authorized", http.StatusForbidden)
			return
		}

		log.Printf("AUTH OK: Service '%s' from IP '%s'", serviceName, clientIP)
		next(w, r)
	}
}

func getClientIP(r *http.Request) string {
	forwarded := r.Header.Get("X-Forwarded-For")
	if forwarded != "" {
		ips := strings.Split(forwarded, ",")
		if len(ips) > 0 {
			return strings.TrimSpace(ips[0])
		}
	}
	realIP := r.Header.Get("X-Real-IP")
	if realIP != "" {
		return realIP
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func isKnownServiceIP(clientIP string, serviceMap *config.ServiceMapConfig) bool {
	for _, services := range serviceMap.Services {
		for _, service := range services {
			if service.Domain != "" && matchesServiceIP(clientIP, service.Domain) {
				return true
			}
		}
	}
	return false
}

func validateServiceAccess(serviceName, clientIP string, serviceMap *config.ServiceMapConfig) bool {
	for _, services := range serviceMap.Services {
		for _, service := range services {
			if service.ID == serviceName {
				return matchesServiceIP(clientIP, service.Domain)
			}
		}
	}
	return false
}

func matchesServiceIP(clientIP, serviceAddr string) bool {
	clientIP = normalizeIP(clientIP)
	serviceAddr = normalizeIP(serviceAddr)

	if serviceAddr == "0.0.0.0" {
		return isLocalhost(clientIP)
	}

	if clientIP == serviceAddr {
		return true
	}

	return isLocalhost(clientIP) && isLocalhost(serviceAddr)
}

func normalizeIP(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "localhost" {
		return "127.0.0.1"
	}
	if ip == "::1" || ip == "[::1]" {
		return "::1"
	}
	return strings.Trim(ip, "[]")
}

func isLocalhost(ip string) bool {
	normalized := normalizeIP(ip)
	localhostIPs := []string{"127.0.0.1", "::1", "0.0.0.0", "localhost"}
	for _, item := range localhostIPs {
		if item == normalized {
			return true
		}
	}
	return false
}
