package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/EasterCompany/dex-event-service/utils"
)

// HandleFabricatorIntent checks if a message has technical intent and triggers the Fabricator if Pro is available.
// Returns (isFabricator, error)
func HandleFabricatorIntent(ctx context.Context, content string, userID string, userName string, channelID string, serverID string, mentionedBot bool, isVoice bool, isCommand bool, deps *Dependencies) (bool, error) {

	const MasterUserID = "313071000877137920"

	// 1. Check Intent
	// Intent detection is now handled by explicit command prefixes (!build) or wake-word analysis in voice.
	// We skip the LLM check here to reduce latency and rely on deterministic triggers.

	// 1.5 Voice Command Safety (Wake Word Enforcement)
	if isVoice {
		lowerContent := strings.ToLower(content)
		hasWakeWord := strings.Contains(lowerContent, "dexter")
		hasCommand := strings.Contains(lowerContent, "fabricate") ||
			strings.Contains(lowerContent, "build") ||
			strings.Contains(lowerContent, "create") ||
			strings.Contains(lowerContent, "implement") ||
			strings.Contains(lowerContent, "construct")

		if !hasWakeWord || !hasCommand {
			log.Printf("Fabricator Intent detected but blocked: Missing wake word ('Dexter') or command word in voice.")
			return false, nil
		}
	}

	log.Printf("Detected FABRICATOR intent for user %s (%s): %s", userName, userID, content)

	// 2. Safety Check: Master User and Mention/Command Requirements
	// Strict ID check is sufficient and more robust than username
	isMaster := userID == MasterUserID

	allowFabrication := false
	reason := ""

	if !isMaster {
		reason = "the triggering user UUID does not match the Master User"
	} else if !isVoice && !mentionedBot && !isCommand {
		reason = "the Master User did not use a command prefix (!build/!fabricate) or mention me (@Dexter)"
	} else {
		allowFabrication = true
	}

	if !allowFabrication {
		// ...

		log.Printf("Fabrication blocked: %s", reason)

		// Generate Verbose Explanation of "How I would do it"

		explanationPrompt := fmt.Sprintf(`The user requested a technical change, but I am restricted from performing it because %s. 



Your task is to provide a verbose, technical, and professional response that:

1. Explains why the request was blocked (be polite but firm about the safety protocol).

2. Provides a detailed, high-fidelity explanation of EXACTLY how you WOULD have performed the action if you were allowed. Include file paths, logic changes, and architectural considerations.

3. Maintain the persona of Dexter: clinical, technical, and helpful.



User Request: %s`, reason, content)

		explanation, _, _ := deps.Ollama.Generate(utils.ResolveModel("public-message", "gpu", "smart"), explanationPrompt, nil)

		if explanation == "" {

			explanation = fmt.Sprintf("I've detected a technical request, but I cannot fulfill it because %s. For safety, technical modifications require direct authorization and proper context.", reason)

		}

		_, _ = deps.Discord.PostMessage(channelID, explanation)

		return true, nil

	}

	// 3. Check Pro Availability

	isProAvailable, resetTime, _ := utils.CheckFabricatorPro()

	if !isProAvailable {

		log.Printf("Fabricator Pro exhausted (Reset: %s). Aborting flow.", resetTime)
		failText := fmt.Sprintf("I'd love to help with that technical task, but my Pro capacity is currently exhausted. It should reset in %s. Since technical changes require high precision, I'd prefer to wait or have you use the CLI for better control.", resetTime)
		if resetTime == "login required" {
			failText = "I can't perform technical tasks right now because my Fabricator session has expired. Please log in again using the CLI."
		}

		// Text response feedback
		_, _ = deps.Discord.PostMessage(channelID, failText)
		return true, nil // Handled (as an error)
	}

	// 3. Acknowledgement
	ackText := "Acknowledged. Initiating the Construction Protocol now. I'll analyze the codebase and implement your request."
	_, _ = deps.Discord.PostMessage(channelID, ackText)

	// 4. Emit Trigger Event
	voiceTriggerEvent := map[string]interface{}{
		"type":          "system.fabricator.voice_trigger", // Reuse same event type for now as FabricatorHandler listens to it
		"transcription": content,
		"user_id":       userID,
		"channel_id":    channelID,
		"server_id":     serverID,
		"timestamp":     time.Now().Unix(),
	}

	reqBody := map[string]interface{}{
		"service": "dex-event-service",
		"event":   voiceTriggerEvent,
	}
	jsonData, _ := json.Marshal(reqBody)
	resp, err := http.Post(deps.EventServiceURL+"/events", "application/json", bytes.NewBuffer(jsonData))
	if err == nil {
		_ = resp.Body.Close()
	}

	// 5. Set Cooldown
	deps.Redis.Set(ctx, fmt.Sprintf("channel:voice_cooldown:%s", channelID), "1", 5*time.Second)

	return true, nil
}

// CleanForSpeech moved from transcription to here if needed elsewhere, but kept there for now.
