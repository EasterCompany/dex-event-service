package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"time"

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

	log.Printf("private-message-handler received event %s (Type: %s) from service %s. Content: %s",
		input.EventID, input.EventType, input.Service, input.EventData["content"])

	// Simulate processing and decision for engagement
	engagementDecision := "engage" // Placeholder: could be "engage", "ignore", "defer"
	reason := "New message in private channel"

	// Construct a child event for engagement decision
	engagementEvent := types.HandlerOutputEvent{
		Type: "engagement.decision",
		Data: map[string]interface{}{
			"decision":        engagementDecision,
			"reason":          reason,
			"handler":         "private-message-handler",
			"event_id":        input.EventID,
			"channel_id":      input.EventData["channel_id"],
			"user_id":         input.EventData["user_id"],
			"message_content": input.EventData["content"],
			"timestamp":       time.Now().Unix(),
		},
	}

	// Construct HandlerOutput
	output := types.HandlerOutput{
		Success: true,
		Events:  []types.HandlerOutputEvent{engagementEvent},
	}

	// Marshal HandlerOutput to JSON and print to stdout
	outputBytes, err := json.Marshal(output)
	if err != nil {
		log.Fatalf("Error marshaling HandlerOutput: %v", err)
	}

	fmt.Println(string(outputBytes))
}
