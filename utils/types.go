package utils

// ServiceReport defines the structure for the /service endpoint response.
type ServiceReport struct {
	Version Version                `json:"version"`
	Health  Health                 `json:"health"`
	Metrics Metrics                `json:"metrics"`
	Logs    []string               `json:"logs"`
	Config  map[string]interface{} `json:"config"`
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
	BuildHash string `json:"build_hash"`
}

// Health represents the health status of the service.
type Health struct {
	Status  string `json:"status"`
	Uptime  string `json:"uptime"`
	Message string `json:"message,omitempty"`
}

// Metrics holds basic performance metrics.
type Metrics struct {
	CPU    string `json:"cpu"`
	Memory string `json:"memory"`
}
