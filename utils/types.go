package utils

import (
	"fmt"
	"strings"
)

// ServiceReport defines the UNIVERSAL structure for the /service endpoint response.
// ALL Dexter services MUST implement this exact structure.
type ServiceReport struct {
	Version Version                `json:"version"`
	Health  Health                 `json:"health"`
	Metrics map[string]interface{} `json:"metrics"`
}

// Version holds all version-related information for a service.
// This structure is UNIVERSAL across all services.
type Version struct {
	Str string         `json:"str"`
	Obj VersionDetails `json:"obj"`
}

// VersionDetails breaks down the version string into its components.
// This structure is UNIVERSAL across all services.
type VersionDetails struct {
	Major     string `json:"major"`
	Minor     string `json:"minor"`
	Patch     string `json:"patch"`
	Branch    string `json:"branch"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	Arch      string `json:"arch"`
	BuildHash string `json:"build_hash"`
}

// Health represents the health status of the service.
// This structure is UNIVERSAL across all services.
type Health struct {
	Status  string `json:"status"`
	Uptime  string `json:"uptime"`
	Message string `json:"message"`
}

// Parse takes a version string and returns a Version object.
func Parse(versionStr string) (*VersionDetails, error) {
	versionStr = strings.TrimPrefix(versionStr, "v") // Always trim 'v' if present
	parts := strings.Split(versionStr, ".")

	// Handle simple "M.m.p" versions, common for cache services or initial states.
	if len(parts) == 3 {
		return &VersionDetails{
			Major: parts[0],
			Minor: parts[1],
			Patch: parts[2],
		}, nil
	}

	// Handle the full "M.m.p.branch.commit.build_date.arch" format.
	if len(parts) != 7 {
		return nil, fmt.Errorf("invalid version string format: expected 3 or 7 parts, got %d for '%s'", len(parts), versionStr)
	}

	return &VersionDetails{
		Major:     parts[0],
		Minor:     parts[1],
		Patch:     parts[2],
		Branch:    parts[3],
		Commit:    parts[4],
		BuildDate: parts[5],
		Arch:      parts[6],
		// BuildHash is no longer part of the string
	}, nil
}
