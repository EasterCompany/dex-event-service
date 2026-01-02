package profiler

import (
	"context"
	"log"
	"strings"
)

// SanitizeAndEnrichProfile cleans up the profile data and injects live data from Discord.

func (h *AnalyzerAgent) SanitizeAndEnrichProfile(ctx context.Context, p *UserProfile, userID string) {

	// 1. Structural Integrity Checks (Default Fields)

	// Ensure Arrays are never nil (to prevent null in JSON)

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

	// 2. Data Enrichment from Discord (Source of Truth)

	if h.DiscordClient != nil && userID != "dexter" {

		member, err := h.DiscordClient.FetchMember(userID)

		if err == nil && member != nil {

			// Update Core Identity

			p.Identity.Username = member.Username

			p.Identity.AvatarURL = member.AvatarURL

			p.Identity.Status = member.Status

			// Add Level Badge if missing

			hasLevelBadge := false

			for _, b := range p.Identity.Badges {

				if b == member.Level {

					hasLevelBadge = true

					break

				}

			}

			if !hasLevelBadge && member.Level != "" {

				p.Identity.Badges = append(p.Identity.Badges, member.Level)

			}

		}

	}

	// 3. Gender Normalization

	g := strings.ToLower(p.Dossier.Identity.Gender)

	switch g {

	case "male", "m", "man", "boy":

		p.Dossier.Identity.Gender = "Male"

	case "female", "f", "woman", "girl":

		p.Dossier.Identity.Gender = "Female"

	default:

		if p.Dossier.Identity.Gender == "" {

			p.Dossier.Identity.Gender = "Unknown"

		}

	}

	// 4. Remove "Unknown" spam (Redundant strings)

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

	// 5. Final Identity Consistency

	if p.Identity.Username == "" {

		if p.Dossier.Identity.FullName != "" {

			p.Identity.Username = p.Dossier.Identity.FullName

		} else {

			p.Identity.Username = "Unknown User"

		}

	}

	log.Printf("[%s] Profile sanitized/enriched for user %s", h.Config.Name, userID)

}
