package utils

import (
	"encoding/json"
	"fmt"
	"os/exec"
)

type TailscaleStatus struct {
	Self Peer            `json:"Self"`
	Peer map[string]Peer `json:"Peer"`
}

type Peer struct {
	ID           string   `json:"ID"`
	HostName     string   `json:"HostName"`
	TailscaleIPs []string `json:"TailscaleIPs"`
	OS           string   `json:"OS"`
	UserID       int      `json:"UserID"`
}

// GetTailscaleStatus runs 'tailscale status --json' and returns the parsed status.
func GetTailscaleStatus() (*TailscaleStatus, error) {
	cmd := exec.Command("tailscale", "status", "--json")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to run tailscale status: %w", err)
	}

	var status TailscaleStatus
	if err := json.Unmarshal(output, &status); err != nil {
		return nil, fmt.Errorf("failed to parse tailscale status: %w", err)
	}

	return &status, nil
}
