package utils

import (
	"math/rand"
	"regexp"
)

// ansiRegex is used to strip ANSI escape codes.
var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// StripANSI removes ANSI color codes from a string.
func StripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
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
