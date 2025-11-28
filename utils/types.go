package utils

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
