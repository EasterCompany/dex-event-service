package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/EasterCompany/dex-event-service/endpoints"
)

func main() {
	http.HandleFunc("/service", endpoints.ServiceHandler)

	port := 8080
	fmt.Printf("Starting dex-event-service on :%d\n", port)
	if err := http.ListenAndServe(fmt.Sprintf(":%d", port), nil); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
