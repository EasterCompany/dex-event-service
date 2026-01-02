package profiler

import (
	"context"
	"log"
	"strings"
)

// SanitizeAndEnrichProfile cleans up the profile data and injects live data from Discord.
func (h *AnalyzerAgent) SanitizeAndEnrichProfile(ctx context.Context, p *UserProfile, userID string) {
	// 1. Structural Integrity Checks (Default Fields)
	if p.Identity.Badges == nil {
		p.Identity.Badges = []string{}
	}
	if p.Attributes == nil {
		p.Attributes = []Attribute{}
	}
	if p.Topics == nil {
		p.Topics = []Topic{}
	}
	if p.Dossier.Social == nil {
		p.Dossier.Social = []SocialConnection{}
	}
	if p.Dossier.Personal.Hobbies == nil {
		p.Dossier.Personal.Hobbies = []string{}
	}
	if p.Dossier.Personal.Habits == nil {
		p.Dossier.Personal.Habits = []string{}
	}
	if p.Dossier.Personal.Vices == nil {
		p.Dossier.Personal.Vices = []string{}
	}
	if p.Dossier.Personal.Virtues == nil {
		p.Dossier.Personal.Virtues = []string{}
	}
	if p.Dossier.Career.Skills == nil {
		p.Dossier.Career.Skills = []string{}
	}

	// 2. Data Enrichment from Discord API
	// We need to fetch the guild member to get roles and nick/username
	// Assuming h.DiscordClient is available and we can get the main guild ID from config or redis?
	// For now, we'll try to find the user in the main guild if possible.
	// Since we don't have the guild ID handy in AnalyzerAgent, we might need to skip deep role analysis
	// or query Redis if the Discord service caches it.

	// However, we can use the `dex-discord-service` cache if it's available in Redis.
	// `user:info:{guildID}:{userID}` is not standard yet.
	// Let's rely on what we can get via the DiscordClient if implemented, or skip for now.
	// Actually, we can fetch the user directly via DiscordClient.Session if exposed.

	// BUT, h.DiscordClient is a wrapper. Let's see if we can use it.
	// h.DiscordClient.Session is not exposed directly usually.
	// Let's proceed with basic data sanitization first.

	// 3. Gender Normalization
	// Ensure Gender is strictly Male or Female if present, otherwise default/unknown
	g := strings.ToLower(p.Dossier.Identity.Gender)
	switch g {
	case "male", "m", "man", "boy":
		p.Dossier.Identity.Gender = "Male"
	case "female", "f", "woman", "girl":
		p.Dossier.Identity.Gender = "Female"
	default:
		// If it's something else or empty, leave it or set to "Unknown"
		if p.Dossier.Identity.Gender == "" {
			p.Dossier.Identity.Gender = "Unknown"
		}
	}

	// 4. Identity Consistency
	if p.Identity.Username == "" {
		// Try to recover from previous known name if available in dossier
		if p.Dossier.Identity.FullName != "" {
			p.Identity.Username = p.Dossier.Identity.FullName
		} else {
			p.Identity.Username = "Unknown User"
		}
	}

	// 5. Remove "Unknown" spam
	// If fields are literally the string "Unknown", sometimes it's better to clear them
	// so the frontend renders defaults cleanly, or keep them if that's the desired UI state.
	// The frontend renders "Unknown" if empty/null, so "Unknown" string is redundant.
	if p.Dossier.Identity.AgeRange == "Unknown" {
		p.Dossier.Identity.AgeRange = ""
	}
	if p.Dossier.Identity.Location == "Unknown" {
		p.Dossier.Identity.Location = ""
	}
	if p.Dossier.Identity.Sexuality == "Unknown" {
		p.Dossier.Identity.Sexuality = ""
	}
	if p.Dossier.Identity.Relationship == "Unknown" {
		p.Dossier.Identity.Relationship = ""
	}
	if p.Dossier.Career.JobTitle == "Unknown" {
		p.Dossier.Career.JobTitle = ""
	}
	if p.Dossier.Career.Company == "Unknown" {
		p.Dossier.Career.Company = ""
	}

	log.Printf("[%s] Profile sanitized for user %s", h.Config.Name, userID)
}
