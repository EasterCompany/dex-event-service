package utils

import (
	sharedUtils "github.com/EasterCompany/dex-go-utils/utils"
)

// Aliases to shared types in dex-go-utils
type ServiceReport = sharedUtils.ServiceReport
type Version = sharedUtils.VersionReport
type VersionDetails = sharedUtils.Version
type Health = sharedUtils.Health
type SystemMetrics = sharedUtils.SystemMetrics
type MetricValue = sharedUtils.MetricValue

// Parse takes a version string and returns a VersionDetails object.
func Parse(versionStr string) (*VersionDetails, error) {
	return sharedUtils.Parse(versionStr)
}
