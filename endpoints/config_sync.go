package endpoints

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"

	"github.com/EasterCompany/dex-event-service/config"
	"github.com/EasterCompany/dex-event-service/utils"
)

func ConfigSyncHandler(w http.ResponseWriter, r *http.Request) {
	file := r.URL.Query().Get("file")
	if file == "" {
		http.Error(w, "file query parameter required", http.StatusBadRequest)
		return
	}

	if file == "system" || file == "system.json" {
		http.Error(w, "access to system.json denied", http.StatusForbidden)
		return
	}

	requesterIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	if requesterIP == "" {
		requesterIP = r.RemoteAddr
	}

	// Get Tailscale Status to know "Me"
	tsStatus, err := utils.GetTailscaleStatus()
	var myIP string
	if err == nil && tsStatus != nil && len(tsStatus.Self.TailscaleIPs) > 0 {
		myIP = tsStatus.Self.TailscaleIPs[0]
	}

	if r.Method == http.MethodPost {
		if file == "service-map" || file == "service-map.json" {
			var newConfig config.ServiceMapConfig
			if err := json.NewDecoder(r.Body).Decode(&newConfig); err != nil {
				http.Error(w, fmt.Sprintf("failed to decode body: %v", err), http.StatusBadRequest)
				return
			}
			if err := config.SaveServiceMap(&newConfig); err != nil {
				http.Error(w, fmt.Sprintf("failed to save config: %v", err), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "only service-map updates are supported via POST", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	switch file {
	case "service-map", "service-map.json":
		cfg, err := config.LoadServiceMap()
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to load service map: %v", err), http.StatusInternalServerError)
			return
		}

		// Transform
		for cat, services := range cfg.Services {
			for i, svc := range services {
				// If service is on Me (127.0.0.1/localhost/0.0.0.0) -> Advertise My Public IP
				if isLocal(svc.Domain) && myIP != "" {
					cfg.Services[cat][i].Domain = myIP
				}
				// If service is on Requester -> Tell them it's Local (127.0.0.1)
				if svc.Domain == requesterIP {
					cfg.Services[cat][i].Domain = "127.0.0.1"
				}
			}
		}
		_ = json.NewEncoder(w).Encode(cfg)

	case "server-map", "server-map.json":
		// Generate dynamic server map
		if tsStatus == nil {
			// Fallback to file if tailscale failed
			cfg, err := config.LoadServerMap()
			if err != nil {
				// Return empty map if file load also fails
				_ = json.NewEncoder(w).Encode(config.ServerMapConfig{Servers: map[string]config.Server{}})
				return
			}
			_ = json.NewEncoder(w).Encode(cfg)
			return
		}

		serverMap := config.ServerMapConfig{
			Servers: make(map[string]config.Server),
		}

		// Add Self
		addPeerToServerMap(serverMap, tsStatus.Self)
		// Add Peers
		for _, peer := range tsStatus.Peer {
			addPeerToServerMap(serverMap, peer)
		}

		// Map services to servers
		svcMap, _ := config.LoadServiceMap()
		if svcMap != nil {
			for _, services := range svcMap.Services {
				for _, svc := range services {
					// Find server with svc.Domain (IP)
					for host, server := range serverMap.Servers {
						// Note: svc.Domain might be 127.0.0.1 for Self.
						// tsStatus.Self.HostName is the hostname of "Me".
						if svc.Domain == server.PublicIPV4 || (isLocal(svc.Domain) && host == tsStatus.Self.HostName) {
							// Check if exists
							exists := false
							for _, existingSvc := range server.Services {
								if existingSvc == svc.ID {
									exists = true
									break
								}
							}
							if !exists {
								server.Services = append(server.Services, svc.ID)
								serverMap.Servers[host] = server
							}
						}
					}
				}
			}
		}

		_ = json.NewEncoder(w).Encode(serverMap)

	case "options", "options.json":
		cfg, err := config.LoadOptions()
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to load options: %v", err), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(cfg)

	default:
		http.Error(w, "unknown config file", http.StatusNotFound)
	}
}

func isLocal(domain string) bool {
	return domain == "127.0.0.1" || domain == "localhost" || domain == "0.0.0.0"
}

func addPeerToServerMap(m config.ServerMapConfig, p utils.Peer) {
	s := config.Server{
		User: "root", // Placeholder
		Key:  "",
	}
	if len(p.TailscaleIPs) > 0 {
		s.PublicIPV4 = p.TailscaleIPs[0]
	}
	if len(p.TailscaleIPs) > 1 {
		s.PublicIPV6 = p.TailscaleIPs[1]
	}
	m.Servers[p.HostName] = s
}
