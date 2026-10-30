//go:build linux

package processproxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const maxProcessEntries = 131072

type linuxScanner struct{}

func newScanner() Scanner { return linuxScanner{} }

func (linuxScanner) Snapshot(ctx context.Context, processName string) (Snapshot, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return Snapshot{}, err
	}
	if len(entries) > maxProcessEntries {
		return Snapshot{}, errors.New("process scan limit exceeded")
	}
	var snapshot Snapshot
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return Snapshot{}, err
		}
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.ParseUint(entry.Name(), 10, 32)
		if err != nil || pid == 0 {
			continue
		}
		processDirectory := filepath.Join("/proc", entry.Name())
		comm, err := readBoundedProcFile(filepath.Join(processDirectory, "comm"), 256)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return Snapshot{}, err
		}
		if strings.TrimSpace(string(comm)) != processName {
			continue
		}
		statusRaw, err := readBoundedProcFile(filepath.Join(processDirectory, "status"), 64<<10)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return Snapshot{}, err
		}
		state, threads, rssBytes, err := parseProcessStatus(string(statusRaw))
		if err != nil {
			return Snapshot{}, err
		}
		comm, err = readBoundedProcFile(filepath.Join(processDirectory, "comm"), 256)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return Snapshot{}, err
		}
		if strings.TrimSpace(string(comm)) != processName {
			continue
		}
		if snapshot.ProcessCount == math.MaxInt || math.MaxUint64-snapshot.ThreadCount < threads || math.MaxUint64-snapshot.RSSBytes < rssBytes {
			return Snapshot{}, errors.New("process counters overflow")
		}
		snapshot.ProcessCount++
		snapshot.ThreadCount += threads
		snapshot.RSSBytes += rssBytes
		switch state {
		case 'R':
			snapshot.RunningCount++
		case 'S', 'D', 'I':
			snapshot.SleepingCount++
		case 'T', 't':
			snapshot.StoppedCount++
		case 'Z':
			snapshot.ZombieCount++
		default:
			snapshot.OtherCount++
		}
	}
	return snapshot, nil
}

func readBoundedProcFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("process metadata exceeds limit")
	}
	return raw, nil
}

func parseProcessStatus(raw string) (byte, uint64, uint64, error) {
	var (
		state       byte
		threads     uint64
		rssBytes    uint64
		stateFound  bool
		threadFound bool
	)
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "State:"):
			fields := strings.Fields(strings.TrimPrefix(line, "State:"))
			if len(fields) == 0 || len(fields[0]) != 1 {
				return 0, 0, 0, errors.New("invalid process state")
			}
			state = fields[0][0]
			stateFound = true
		case strings.HasPrefix(line, "Threads:"):
			value, err := parseProcessCounter(strings.TrimPrefix(line, "Threads:"), false)
			if err != nil || value < 1 {
				return 0, 0, 0, errors.New("invalid process thread count")
			}
			threads = value
			threadFound = true
		case strings.HasPrefix(line, "VmRSS:"):
			value, err := parseProcessCounter(strings.TrimPrefix(line, "VmRSS:"), true)
			if err != nil {
				return 0, 0, 0, errors.New("invalid process RSS")
			}
			rssBytes = value
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, 0, err
	}
	if !stateFound || !threadFound {
		return 0, 0, 0, errors.New("incomplete process metadata")
	}
	return state, threads, rssBytes, nil
}

func parseProcessCounter(raw string, kibibytes bool) (uint64, error) {
	fields := strings.Fields(raw)
	if len(fields) == 0 || (kibibytes && (len(fields) != 2 || fields[1] != "kB")) || (!kibibytes && len(fields) != 1) {
		return 0, errors.New("invalid process counter")
	}
	value, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse process counter: %w", err)
	}
	if kibibytes {
		if value > math.MaxUint64/1024 {
			return 0, errors.New("process counter overflow")
		}
		value *= 1024
	}
	return value, nil
}
