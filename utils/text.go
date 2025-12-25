package utils

import (
	"regexp"
)

// ansiRegex is used to strip ANSI escape codes.
var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// StripANSI removes ANSI color codes from a string.
func StripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}
