package utils

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ServiceReport defines the UNIVERSAL structure for the /service endpoint response.
type ServiceReport struct {
	Version VersionReport          `json:"version"`
	Health  Health                 `json:"health"`
	Metrics map[string]interface{} `json:"metrics"`
}

// VersionReport holds all version-related information for a service.
type VersionReport struct {
	Str string  `json:"str"`
	Obj Version `json:"obj"`
}

// Version holds the components of a parsed version string.
type Version struct {
	Major     string `json:"major"`
	Minor     string `json:"minor"`
	Patch     string `json:"patch"`
	Branch    string `json:"branch"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	Arch      string `json:"arch"`
	BuildHash string `json:"build_hash,omitempty"`
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
	BuildHash    = ""
)

// SetVersion sets the version information for the service.
func SetVersion(version, branch, commit, buildDate, arch string) {
	VersionStr = version
	GitBranch = branch
	GitCommit = commit
	BuildDate = buildDate
	Architecture = arch
}

// GetVersion returns the VersionReport for the service.
func GetVersion() VersionReport {
	major, minor, patch, _ := ParseVersionTag(VersionStr)
	// Standard 7-part version label: MAJOR.MINOR.PATCH.BRANCH.COMMIT.DATETIME.ARCH
	fullStr := FormatFull(VersionStr, GitBranch, GitCommit, BuildDate, Architecture)

	v := Version{
		Major:     fmt.Sprintf("%d", major),
		Minor:     fmt.Sprintf("%d", minor),
		Patch:     fmt.Sprintf("%d", patch),
		Branch:    GitBranch,
		Commit:    GitCommit,
		BuildDate: BuildDate,
		Arch:      Architecture,
		BuildHash: BuildHash,
	}

	return VersionReport{
		Str: fullStr,
		Obj: v,
	}
}

// FormatFull constructs a standardized 7-part version string.
func FormatFull(version, branch, commit, buildDate, arch string) string {
	versionClean := strings.TrimPrefix(version, "v")
	return fmt.Sprintf("%s.%s.%s.%s.%s", versionClean, branch, commit, buildDate, arch)
}

// ParseVersionTag parses a version tag string (e.g., "1.2.3") into its major, minor, and patch components.
func ParseVersionTag(tag string) (int, int, int, error) {
	trimmedTag := strings.TrimPrefix(tag, "v")
	parts := strings.Split(trimmedTag, ".")
	if len(parts) < 3 {
		return 0, 0, 0, fmt.Errorf("tag does not have at least 3 parts separated by '.'")
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

// Parse takes a version string and returns a Version object.
// It is robust against leading/trailing whitespace and extraneous log text.
func Parse(versionStr string) (*Version, error) {
	versionStr = strings.TrimSpace(versionStr)
	versionStr = strings.TrimPrefix(versionStr, "v")

	// If the string contains spaces (like log messages), try to find the version part.
	if strings.Contains(versionStr, " ") {
		words := strings.Fields(versionStr)
		for i := len(words) - 1; i >= 0; i-- {
			if strings.Count(words[i], ".") >= 2 {
				versionStr = words[i]
				break
			}
		}
	}

	parts := strings.Split(versionStr, ".")

	if len(parts) == 3 {
		return &Version{
			Major: parts[0],
			Minor: parts[1],
			Patch: parts[2],
		}, nil
	}

	if len(parts) != 7 && len(parts) != 8 {
		return nil, fmt.Errorf("invalid version string format: expected 3, 7 or 8 parts, got %d for '%s'", len(parts), versionStr)
	}

	v := &Version{
		Major:     parts[0],
		Minor:     parts[1],
		Patch:     parts[2],
		Branch:    parts[3],
		Commit:    parts[4],
		BuildDate: parts[5],
		Arch:      parts[6],
	}

	if len(parts) == 8 {
		v.BuildHash = parts[7]
	}

	return v, nil
}

// String formats a Version object back into a version string.
func (v *Version) String() string {
	if v.Branch == "" || v.Commit == "" {
		return v.Short()
	}
	if v.BuildHash == "" {
		return fmt.Sprintf("%s.%s.%s.%s.%s.%s.%s",
			v.Major, v.Minor, v.Patch,
			v.Branch, v.Commit, v.BuildDate, v.Arch,
		)
	}
	return fmt.Sprintf("%s.%s.%s.%s.%s.%s.%s.%s",
		v.Major, v.Minor, v.Patch,
		v.Branch, v.Commit, v.BuildDate, v.Arch, v.BuildHash,
	)
}

// Short returns the MAJOR.MINOR.PATCH part of the version.
func (v *Version) Short() string {
	return fmt.Sprintf("%s.%s.%s", v.Major, v.Minor, v.Patch)
}

// Compare compares two Version objects based on their MAJOR.MINOR.PATCH numbers.
func (v *Version) Compare(other *Version) int {
	vMajor, _ := strconv.Atoi(v.Major)
	oMajor, _ := strconv.Atoi(other.Major)
	if vMajor != oMajor {
		if vMajor < oMajor {
			return -1
		}
		return 1
	}

	vMinor, _ := strconv.Atoi(v.Minor)
	oMinor, _ := strconv.Atoi(other.Minor)
	if vMinor != oMinor {
		if vMinor < oMinor {
			return -1
		}
		return 1
	}

	vPatch, _ := strconv.Atoi(v.Patch)
	oPatch, _ := strconv.Atoi(other.Patch)
	if vPatch != oPatch {
		if vPatch < oPatch {
			return -1
		}
		return 1
	}

	return 0
}

// FetchLatestVersionFromURL fetches the latest version string from a URL.
func FetchLatestVersionFromURL(url string) (string, error) {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("failed to fetch version from %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code %d from %s", resp.StatusCode, url)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	version := strings.TrimSpace(string(body))
	if version == "" {
		return "", fmt.Errorf("empty version string from %s", url)
	}

	return version, nil
}

// IncrementVersion takes a version and increment type, returns the new incremented version.
func IncrementVersion(major, minor, patch int, incrementType string) (int, int, int, error) {
	switch incrementType {
	case "major":
		return major + 1, 0, 0, nil
	case "minor":
		return major, minor + 1, 0, nil
	case "patch":
		return major, minor, patch + 1, nil
	default:
		return 0, 0, 0, fmt.Errorf("invalid increment type: %s (must be 'major', 'minor', or 'patch')", incrementType)
	}
}
