package utils

import (
	"fmt"
	"strconv"
	"strings"
)

// ServiceReport defines the UNIVERSAL structure for the /service endpoint response.
type ServiceReport struct {
	Version Version                `json:"version"`
	Health  Health                 `json:"health"`
	Metrics map[string]interface{} `json:"metrics"`
}

// Version holds all version-related information for a service.
type Version struct {
	Str string         `json:"str"`
	Obj VersionDetails `json:"obj"`
}

// VersionDetails breaks down the version string into its components.
type VersionDetails struct {
	Major     string `json:"major"`
	Minor     string `json:"minor"`
	Patch     string `json:"patch"`
	Branch    string `json:"branch"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	Arch      string `json:"arch"`
}

// Health represents the health status of the service.
type Health struct {
	Status  string `json:"status"`
	Uptime  string `json:"uptime"`
	Message string `json:"message"`
}

var (
	// Version information will be injected by the build process
	VersionStr   = "0.0.0"
	BuildDate    = "unknown"
	GitCommit    = "unknown"
	GitBranch    = "unknown"
	Architecture = "unknown"
)

// SetVersion sets the version information for the service.
// STRICTLY enforces the 7-part version standard: MAJOR.MINOR.PATCH.BRANCH.COMMIT.DATE.ARCH
// The build tool injects these values.
func SetVersion(version, branch, commit, buildDate, arch string) {
	VersionStr = version
	GitBranch = branch
	GitCommit = commit
	BuildDate = buildDate
	Architecture = arch
}

// GetVersion returns the version information for the service.
func GetVersion() Version {
	major, minor, patch, _ := ParseVersionTag(VersionStr)
	// Standard 7-part version label: MAJOR.MINOR.PATCH.BRANCH.COMMIT.DATETIME.ARCH
	fullStr := fmt.Sprintf("%d.%d.%d.%s.%s.%s.%s", major, minor, patch, GitBranch, GitCommit, BuildDate, Architecture)
	return Version{
		Str: fullStr,
		Obj: VersionDetails{
			Major:     fmt.Sprintf("%d", major),
			Minor:     fmt.Sprintf("%d", minor),
			Patch:     fmt.Sprintf("%d", patch),
			Branch:    GitBranch,
			Commit:    GitCommit,
			BuildDate: BuildDate,
			Arch:      Architecture,
		},
	}
}

// ParseVersionTag is a helper function to parse version tags.
func ParseVersionTag(tag string) (int, int, int, error) {
	trimmedTag := strings.TrimPrefix(tag, "v")
	parts := strings.Split(trimmedTag, ".")
	if len(parts) < 3 {
		return 0, 0, 0, fmt.Errorf("tag does not have at least 3 parts separated by '.'")
	}

	major, _ := strconv.Atoi(parts[0])
	minor, _ := strconv.Atoi(parts[1])
	patch, _ := strconv.Atoi(parts[2])

	return major, minor, patch, nil
}

// Parse takes a version string and returns a VersionDetails object.
func Parse(versionStr string) (*VersionDetails, error) {
	versionStr = strings.TrimPrefix(versionStr, "v")
	parts := strings.Split(versionStr, ".")

	if len(parts) == 3 {
		return &VersionDetails{
			Major: parts[0],
			Minor: parts[1],
			Patch: parts[2],
		}, nil
	}

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
	}, nil
}
