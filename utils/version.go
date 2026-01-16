package utils

import (
	"fmt"
	"strconv"
	"strings"
)

var (
	// Version information will be injected by the build process
	VersionStr   = "0.0.0"
	BuildDate    = "unknown"
	GitCommit    = "unknown"
	GitBranch    = "unknown"
	GitTag       = "unknown"
	Architecture = "unknown"
	BuildHash    = "unknown"
)

// SetVersion sets the version information for the service.
func SetVersion(version, branch, commit, buildDate, buildYear, buildHash, arch string) {
	VersionStr = version
	GitBranch = branch
	GitCommit = commit
	BuildDate = buildDate
	BuildHash = buildHash
	Architecture = arch
}

// GetVersion returns the version information for the service.
func GetVersion() Version {
	major, minor, patch, _ := ParseVersionTag(VersionStr)
	fullStr := fmt.Sprintf("%s.%s.%s.%s.%s.%s", VersionStr, GitBranch, GitCommit, BuildDate, Architecture, BuildHash)
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
			BuildHash: BuildHash,
		},
	}
}

// ParseVersionTag is a helper function to parse version tags.
// It is duplicated here to avoid circular dependencies.
func ParseVersionTag(tag string) (int, int, int, error) {
	trimmedTag := strings.TrimPrefix(tag, "v")
	parts := strings.Split(trimmedTag, ".")
	if len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("tag does not have 3 parts separated by '.'")
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to parse major version '%s': %w", parts[0], err)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to parse minor version '%s': %w", parts[1], err)
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to parse patch version '%s': %w", parts[2], err)
	}

	return major, minor, patch, nil
}
