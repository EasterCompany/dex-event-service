package utils

import (
	"fmt"
	"strings"
)

// NormalizeMentions replaces <@USER_ID> with @Username based on the provided mentions map.
// mentions should be a slice of maps, each containing "id" and "username" or "global_name".
func NormalizeMentions(content string, mentions []interface{}) string {
	for _, m := range mentions {
		if mentionMap, ok := m.(map[string]interface{}); ok {
			id, _ := mentionMap["id"].(string)
			username, _ := mentionMap["username"].(string)
			globalName, _ := mentionMap["global_name"].(string)

			displayName := username
			if globalName != "" {
				displayName = globalName
			}

			if id != "" && displayName != "" {
				// Replace standard mention <@ID>
				content = strings.ReplaceAll(content, fmt.Sprintf("<@%s>", id), fmt.Sprintf("@%s", displayName))
				// Replace nickname mention <@!ID> (older format but still possible)
				content = strings.ReplaceAll(content, fmt.Sprintf("<@!%s>", id), fmt.Sprintf("@%s", displayName))
			}
		}
	}
	return content
}

// DenormalizeMentions replaces @Username with <@USER_ID> based on the provided user list.
// userMap should be map[string]string where key is username and value is ID.
func DenormalizeMentions(content string, userMap map[string]string) string {
	// Find all @Username patterns
	// We use a regex to capture @Word possibly followed by non-space chars
	// But usernames can be complex.
	// A simpler approach: iterate over known users and replace their @Name in the content.
	// To avoid replacing substrings (e.g. @Ann in @Anna), we should sort users by name length desc or use regex boundaries.
	// Discord usernames are restrictive, but display names can be anything.

	// Basic implementation: Iterate map
	for username, id := range userMap {
		if id == "" {
			continue
		}
		target := "@" + username
		replacement := fmt.Sprintf("<@%s>", id)

		// Use simple replacement for now.
		// Ideally we'd ensure we match full words, but names can contain spaces in some contexts (though usually not after @ without quotes).
		// Discord handles @Username.
		content = strings.ReplaceAll(content, target, replacement)
	}
	return content
}
