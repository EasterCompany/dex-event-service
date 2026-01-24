package utils

import (
	"math/rand"
	"regexp"
	"strings"
)

// ansiRegex is used to strip ANSI escape codes.
var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// StripANSI removes ANSI color codes from a string.
func StripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}

// StripEmojis removes unicode emojis and Discord custom emoji tags from a string.
func StripEmojis(s string) string {
	// 1. Remove Discord custom emojis: <:name:id> or <a:name:id>
	customEmojiRegex := regexp.MustCompile(`<a?:\w+:\d+>`)
	s = customEmojiRegex.ReplaceAllString(s, "")

	// 2. Remove Unicode emojis
	// This regex covers most common emoji ranges
	unicodeEmojiRegex := regexp.MustCompile(`[\x{1F300}-\x{1F9FF}]|[\x{2600}-\x{26FF}]`)
	s = unicodeEmojiRegex.ReplaceAllString(s, "")

	return strings.TrimSpace(s)
}

// NormalizeWhitespace aggressively tightens a string for AI context.
// It trims the entire string, collapses 3+ newlines to 2, and trims horizontal whitespace from every line.
func NormalizeWhitespace(s string) string {
	// 1. Remove horizontal whitespace from start/end of every line
	reLines := regexp.MustCompile(`(?m)^[ 	]+|[ 	]+$`)
	s = reLines.ReplaceAllString(s, "")

	// 2. Collapse 3+ newlines down to 2
	reNewlines := regexp.MustCompile(`\n{3,}`)
	s = reNewlines.ReplaceAllString(s, "\n\n")

	// 3. Final trim
	return strings.TrimSpace(s)
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
