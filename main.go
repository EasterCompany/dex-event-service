package main

import (
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/EasterCompany/dex-event-service/config"
	"github.com/EasterCompany/dex-event-service/endpoints"
	"github.com/EasterCompany/dex-event-service/utils"
)

const ServiceName = "dex-event-service"

var (
	version   string
	branch    string
	commit    string
	buildDate string
	buildYear string
	buildHash string
	arch      string
)

func main() {
	// Set the version for the service.
	utils.SetVersion(version, branch, commit, buildDate, buildYear, buildHash, arch)

	// Load the service map and find our own configuration.
	serviceMap, err := config.LoadServiceMap()
	if err != nil {
		log.Fatalf("FATAL: Could not load service-map.json: %v", err)
	}

	var selfConfig *config.ServiceEntry
	for _, service := range serviceMap.Services["cs"] {
		if service.ID == ServiceName {
			selfConfig = &service
			break
		}
	}

	if selfConfig == nil {
		log.Fatalf("FATAL: Service '%s' not found in service-map.json. Shutting down.", ServiceName)
	}

	// Get port from config, convert to integer.
	port, err := strconv.Atoi(selfConfig.Port)
	if err != nil {
		log.Fatalf("FATAL: Invalid port '%s' for service '%s' in service-map.json: %v", selfConfig.Port, ServiceName, err)
	}

	// Register handlers and start the server.
	http.HandleFunc("/service", endpoints.ServiceHandler)

	fmt.Printf("Starting %s on :%d\n", ServiceName, port)
	if err := http.ListenAndServe(fmt.Sprintf(":%d", port), nil); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
