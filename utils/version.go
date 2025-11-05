package utils

import (
	"fmt"
	"runtime"
)

// These should be set at build time using -ldflags
var (
	VersionMajor = "0"
	VersionMinor = "0"
	VersionPatch = "1"
	Branch       = "main"
	Commit       = "dev"
	BuildDate    = "unknown"
	BuildHash    = "unknown"
)

// GetVersion constructs and returns the version information for the service.
func GetVersion() Version {
	// In a real scenario, these values would be dynamically populated.
	// For now, we use the build-time variables and some placeholders.
	commitShort := Commit
	if len(Commit) > 7 {
		commitShort = Commit[:7]
	}

	vObj := VersionObject{
		Major:     VersionMajor,
		Minor:     VersionMinor,
		Patch:     VersionPatch,
		Branch:    Branch,
		Commit:    commitShort,
		BuildDate: BuildDate,
		Arch:      fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		BuildHash: BuildHash,
	}

	tag := fmt.Sprintf("%s.%s.%s", vObj.Major, vObj.Minor, vObj.Patch)
	str := fmt.Sprintf("%s-%s+%s.%s.%s.%s",
		tag,
		vObj.Branch,
		vObj.Commit,
		vObj.BuildDate,
		vObj.Arch,
		vObj.BuildHash,
	)

	return Version{
		Tag: tag,
		Str: str,
		Obj: vObj,
	}
}
