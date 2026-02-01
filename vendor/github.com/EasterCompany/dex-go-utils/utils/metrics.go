package utils

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// SystemMetrics holds CPU and Memory usage statistics
type SystemMetrics struct {
	CPU    MetricValue `json:"cpu"`
	Memory MetricValue `json:"memory"`
}

// ToMap converts SystemMetrics to a map[string]interface{}
func (s SystemMetrics) ToMap() map[string]interface{} {
	return map[string]interface{}{
		"cpu": map[string]interface{}{
			"avg": s.CPU.Avg,
		},
		"memory": map[string]interface{}{
			"avg": s.Memory.Avg,
		},
	}
}

// MetricValue represents a single metric with average value
type MetricValue struct {
	Avg float64 `json:"avg"`
}

// GetMetrics returns current CPU and Memory usage metrics for the current process
// and any optional additional PIDs (like child processes).
func GetMetrics(pids ...int) SystemMetrics {
	totalCPU, totalMem := GetPIDStats(os.Getpid())

	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		cpu, mem := GetPIDStats(pid)
		totalCPU += cpu
		totalMem += mem
	}

	return SystemMetrics{
		CPU:    MetricValue{Avg: totalCPU},
		Memory: MetricValue{Avg: totalMem},
	}
}

// GetPIDStats returns CPU and Memory (MB) for a specific PID.
func GetPIDStats(pid int) (cpu float64, memMB float64) {
	// Use 'ps' to get CPU percentage and RSS (Resident Set Size) in KB
	cmd := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "%cpu,rss", "--no-headers")
	output, err := cmd.Output()
	if err != nil {
		return 0, 0
	}

	fields := strings.Fields(string(output))
	if len(fields) >= 2 {
		cpu, _ = strconv.ParseFloat(fields[0], 64)
		memKB, _ := strconv.ParseFloat(fields[1], 64)
		memMB = memKB / 1024.0
	}

	return cpu, memMB
}
