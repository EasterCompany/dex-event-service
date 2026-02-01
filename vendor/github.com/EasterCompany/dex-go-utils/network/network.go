package network

import (
	"net"
)

// GetLocalIPs returns a map of all IP addresses associated with the local machine.
func GetLocalIPs() map[string]bool {
	ips := make(map[string]bool)
	ips["127.0.0.1"] = true
	ips["localhost"] = true
	ips["0.0.0.0"] = true
	ips["::1"] = true

	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok {
				ips[ipnet.IP.String()] = true
			}
		}
	}
	return ips
}

// IsAddressLocal checks if the given domain/IP is the local machine.
func IsAddressLocal(domain string) bool {
	if domain == "" || domain == "127.0.0.1" || domain == "localhost" || domain == "::1" {
		return true
	}
	localIPs := GetLocalIPs()
	return localIPs[domain]
}
