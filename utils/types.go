package utils

import (
	sharedUtils "github.com/EasterCompany/dex-go-utils/utils"
)

// Aliases to shared types in dex-go-utils
type ServiceReport = sharedUtils.ServiceReport
type Version = sharedUtils.Version
type VersionDetails = sharedUtils.VersionDetails
type Health = sharedUtils.Health

// Parse takes a version string and returns a VersionDetails object.
func Parse(versionStr string) (*VersionDetails, error) {
	return sharedUtils.Parse(versionStr)
}
