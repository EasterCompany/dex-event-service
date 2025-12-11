package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/EasterCompany/dex-event-service/types"
)

func main() {
	// Read HandlerInput from stdin
	inputBytes, err := io.ReadAll(os.Stdin)
	if err != nil {
		log.Fatalf("Error reading stdin: %v", err)
	}

	var input types.HandlerInput
	if err := json.Unmarshal(inputBytes, &input); err != nil {
		log.Fatalf("Error unmarshaling HandlerInput: %v", err)
	}

	log.Printf("Webhook handler received event: %s", input.EventID)

	// In the future, this is where we'd parse the webhook content and maybe post a summary
	// For now, simply acknowledge the webhook was received and processed.
	// We no longer emit webhook.processed events to avoid spam.

	// Construct HandlerOutput
	output := types.HandlerOutput{
		Success: true,
		Events:  []types.HandlerOutputEvent{},
	}

	// Marshal HandlerOutput to JSON and print to stdout
	outputBytes, err := json.Marshal(output)
	if err != nil {
		log.Fatalf("Error marshaling HandlerOutput: %v", err)
	}

	fmt.Println(string(outputBytes))
}
