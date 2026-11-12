package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// usageSnapshotVersion is the on-disk format of the lifetime counters. Bump it
// when a change makes an older file unsafe to load.
const usageSnapshotVersion = 1

// persistedUsage is the part of Metrics that survives a restart: lifetime
// request counters, token usage and per-server-key usage. The minute buckets and
// the recent-request rings are intentionally not persisted; they only describe
// the last hour of the current process.
type persistedUsage struct {
	Version         int         `json:"version"`
	Total           uint64      `json:"total"`
	Success         uint64      `json:"success"`
	Errors          uint64      `json:"errors"`
	FreeLimitEvents uint64      `json:"free_limit_events"`
	Usage           UsagePeriod `json:"usage"`
}

// SaveUsage writes the lifetime counters to path atomically (temporary file in
// the same directory, then rename), so a crash mid-write cannot leave a
// half-written snapshot that a later start would load as truth. A write error
// must never stop the proxy from serving.
func (m *Metrics) SaveUsage(path string) error {
	if m == nil || path == "" {
		return nil
	}
	m.mu.Lock()
	state := persistedUsage{
		Version:         usageSnapshotVersion,
		Total:           m.total.Load(),
		Success:         m.success.Load(),
		Errors:          m.errors.Load(),
		FreeLimitEvents: m.freeLimitEvents.Load(),
		Usage:           m.lifetimeUsage,
	}
	// Marshal under the lock: the maps are shared with Record, and copying
	// them only to marshal outside the lock would double the allocation.
	data, err := json.Marshal(state)
	m.mu.Unlock()
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".usage-*.tmp")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer func() {
		// A successful rename removed tmp; this only cleans up failures.
		_ = os.Remove(tmp)
	}()
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Chmod(0o644); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadUsage restores a snapshot written by SaveUsage. A missing file is not an
// error (first start). A corrupt or newer-version file is reported and leaves
// the counters untouched, so the proxy still starts with a clean slate.
func (m *Metrics) LoadUsage(path string) error {
	if m == nil || path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var state persistedUsage
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	if state.Version > usageSnapshotVersion {
		return &unsupportedUsageVersion{version: state.Version}
	}
	ensureUsageMaps(&state.Usage)

	m.mu.Lock()
	m.total.Store(state.Total)
	m.success.Store(state.Success)
	m.errors.Store(state.Errors)
	m.freeLimitEvents.Store(state.FreeLimitEvents)
	m.lifetimeUsage = state.Usage
	m.mu.Unlock()
	return nil
}

type unsupportedUsageVersion struct{ version int }

func (e *unsupportedUsageVersion) Error() string {
	return fmt.Sprintf("usage snapshot version %d is newer than this build", e.version)
}

func ensureUsageMaps(usage *UsagePeriod) {
	if usage.Models == nil {
		usage.Models = map[string]TokenCounts{}
	}
	if usage.Tiers == nil {
		usage.Tiers = map[string]TokenCounts{}
	}
	if usage.Sessions == nil {
		usage.Sessions = map[string]TokenCounts{}
	}
	if usage.ServerKeys == nil {
		usage.ServerKeys = map[string]ServerKeyUsage{}
	}
}

// StartUsageSaver writes the snapshot every interval and one last time when ctx
// is cancelled. Save failures are logged and otherwise ignored: losing metering
// data must never take the service down.
func (m *Metrics) StartUsageSaver(ctx context.Context, path string, interval time.Duration, logger *slog.Logger) {
	if m == nil || path == "" {
		return
	}
	if interval <= 0 {
		interval = time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		save := func() {
			if err := m.SaveUsage(path); err != nil && logger != nil {
				logger.Warn("usage snapshot write failed", "component", "telemetry", "event", "usage_save_failed", "error", err)
			}
		}
		for {
			select {
			case <-ctx.Done():
				save()
				return
			case <-ticker.C:
				save()
			}
		}
	}()
}
