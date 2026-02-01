package utils

import (
	sharedUtils "github.com/EasterCompany/dex-go-utils/utils"
	"math/rand"
)

// StripANSI removes ANSI color codes from a string.
func StripANSI(s string) string {
	return sharedUtils.StripANSI(s)
}

// StripEmojis removes unicode emojis and Discord custom emoji tags from a string.
func StripEmojis(s string) string {
	return sharedUtils.StripEmojis(s)
}

// NormalizeWhitespace aggressively tightens a string for AI context.
func NormalizeWhitespace(s string) string {
	return sharedUtils.NormalizeWhitespace(s)
}

// GetLoadingMessage returns a random "thinking" message with the custom typing emoji.
func GetLoadingMessage() string {
	emoji := "<a:typing:1449387367315275786>"
	phrases := []string{
		"Analyzing system parameters...",
		"Checking the neural buffers...",
		"Spinning up the GPU...",
		"Reviewing source code...",
		"Consulting the archives...",
		"One moment, compiling thoughts...",
		"Querying the knowledge base...",
		"Running heuristics...",
		"Optimizing context window...",
		"Aligning vectors...",
		"Thinking...",
		"Processing request...",
	}

	phrase := phrases[rand.Intn(len(phrases))]
	return emoji + " *" + phrase + "*"
}
