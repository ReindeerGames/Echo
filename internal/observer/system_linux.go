//go:build linux

package observer

import (
	"bufio"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"os"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type linuxSystemReader struct{}

func newSystemReader() SystemReader { return linuxSystemReader{} }

func (linuxSystemReader) Snapshot(diskPath string) (SystemSnapshot, error) {
	memoryRaw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return SystemSnapshot{}, err
	}
	totalMemory, availableMemory, err := parseMemInfo(string(memoryRaw))
	if err != nil {
		return SystemSnapshot{}, err
	}
	loadRaw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return SystemSnapshot{}, err
	}
	load1, load5, load15, err := parseLoadAverage(string(loadRaw))
	if err != nil {
		return SystemSnapshot{}, err
	}
	uptimeRaw, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return SystemSnapshot{}, err
	}
	uptime, err := parseUptime(string(uptimeRaw))
	if err != nil {
		return SystemSnapshot{}, err
	}
	var filesystem unix.Statfs_t
	if err := unix.Statfs(diskPath, &filesystem); err != nil {
		return SystemSnapshot{}, err
	}
	if filesystem.Bsize <= 0 || filesystem.Blocks == 0 {
		return SystemSnapshot{}, errors.New("filesystem returned no capacity")
	}
	blockSize := uint64(filesystem.Bsize)
	totalOverflow, diskTotal := bits.Mul64(filesystem.Blocks, blockSize)
	availableOverflow, diskAvailable := bits.Mul64(filesystem.Bavail, blockSize)
	if totalOverflow != 0 || availableOverflow != 0 || diskAvailable > diskTotal {
		return SystemSnapshot{}, errors.New("invalid filesystem counters")
	}
	cpuCount := runtime.NumCPU()
	if cpuCount < 1 {
		cpuCount = 1
	}
	return SystemSnapshot{
		DiskPath:             diskPath,
		DiskTotalBytes:       diskTotal,
		DiskAvailableBytes:   diskAvailable,
		DiskUsedPercent:      percentUsed(diskTotal, diskAvailable),
		MemoryTotalBytes:     totalMemory,
		MemoryAvailableBytes: availableMemory,
		MemoryUsedPercent:    percentUsed(totalMemory, availableMemory),
		Load1:                load1,
		Load5:                load5,
		Load15:               load15,
		CPUCount:             cpuCount,
		Load1PerCPU:          load1 / float64(cpuCount),
		UptimeSeconds:        uptime,
	}, nil
}

func parseMemInfo(raw string) (uint64, uint64, error) {
	values := map[string]uint64{}
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimSuffix(fields[0], ":")
		if name != "MemTotal" && name != "MemAvailable" {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("parse %s: %w", name, err)
		}
		if value > math.MaxUint64/1024 {
			return 0, 0, fmt.Errorf("%s counter overflow", name)
		}
		values[name] = value * 1024
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, err
	}
	if values["MemTotal"] == 0 || values["MemAvailable"] > values["MemTotal"] {
		return 0, 0, errors.New("invalid memory counters")
	}
	return values["MemTotal"], values["MemAvailable"], nil
}

func parseLoadAverage(raw string) (float64, float64, float64, error) {
	fields := strings.Fields(raw)
	if len(fields) < 3 {
		return 0, 0, 0, errors.New("invalid load average counters")
	}
	values := make([]float64, 3)
	for index := range values {
		value, err := strconv.ParseFloat(fields[index], 64)
		if err != nil || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return 0, 0, 0, errors.New("invalid load average counters")
		}
		values[index] = value
	}
	return values[0], values[1], values[2], nil
}

func parseUptime(raw string) (float64, error) {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return 0, errors.New("invalid uptime counter")
	}
	value, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, errors.New("invalid uptime counter")
	}
	return value, nil
}

func percentUsed(total, available uint64) float64 {
	if total == 0 || available >= total {
		if available == total {
			return 0
		}
		return 100
	}
	return (float64(total-available) / float64(total)) * 100
}
