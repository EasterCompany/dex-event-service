package greeting

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"math/rand"
	"net/http"
	"time"

	"github.com/EasterCompany/dex-event-service/internal/handlers"
	"github.com/EasterCompany/dex-event-service/types"
)

func Handle(ctx context.Context, input types.HandlerInput, deps *handlers.Dependencies) (types.HandlerOutput, error) {
	channelID, _ := input.EventData["channel_id"].(string)
	channelName, _ := input.EventData["channel_name"].(string)

	log.Printf("greeting-handler: Bot joined channel %s (%s)", channelName, channelID)

	greetings := []string{
		"Hey",
		"Hello",
		"Yo",
		"Hi there",
		"Greetings",
		"What's up",
		"How's it going",
	}
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	greeting := greetings[r.Intn(len(greetings))]

	log.Printf("Selected greeting: %s", greeting)

	deps.Discord.UpdateBotStatus("Greeting...", "online", 0)

	ttsPayload := map[string]string{"text": greeting, "language": "en"}
	ttsJson, _ := json.Marshal(ttsPayload)

	ttsResp, ttsErr := http.Post(deps.TTSServiceURL+"/generate", "application/json", bytes.NewBuffer(ttsJson))
	if ttsErr != nil {
		log.Printf("TTS generation failed: %v", ttsErr)
	} else {
		defer func() { _ = ttsResp.Body.Close() }()
		if ttsResp.StatusCode == 200 {
			audioBytes, _ := io.ReadAll(ttsResp.Body)

			time.Sleep(500 * time.Millisecond)

			deps.Discord.UpdateBotStatus("Speaking...", "online", 0)
			if playErr := deps.Discord.PlayAudio(audioBytes); playErr != nil {
				log.Printf("Failed to play audio in Discord: %v", playErr)
			}
		} else {
			log.Printf("TTS service error: %d", ttsResp.StatusCode)
		}
	}

	deps.Discord.UpdateBotStatus("Listening for events...", "online", 2)

	return types.HandlerOutput{
		Success: true,
		Events:  []types.HandlerOutputEvent{},
	}, nil
}
