package brain

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/neverprepared/mcp-phantom-brain/internal/config"
)

// CachedSnapshot describes a snapshot that was previously downloaded
// from the daemon and stored locally. Used during degraded birth
// (snapshot fetch fails -> fall back to the most recent cache) and to
// surface staleness in brain_status output. The tarball itself sits
// alongside this metadata as snapshot-<gen>.tar.zst.
type CachedSnapshot struct {
	Gen      uint64 `json:"gen"`
	SHA256   string `json:"sha256"`
	SizeBytes int64 `json:"size_bytes"`
	FetchedAt string `json:"fetched_at"` // RFC3339
	TarballPath string `json:"tarball_path"`
}

// ListCachedSnapshots returns every cached snapshot under
// SnapshotCacheDir(), sorted newest-first by generation number. Used
// by birth's degraded path to choose a seed and by operator tools to
// report cache state.
func ListCachedSnapshots(cfg *config.Agent) ([]CachedSnapshot, error) {
	if cfg == nil {
		return nil, errors.New("brain: ListCachedSnapshots requires a non-nil Agent")
	}
	root := cfg.SnapshotCacheDir()
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("brain: read snapshot cache: %w", err)
	}
	var out []CachedSnapshot
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".manifest.json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, e.Name()))
		if err != nil {
			continue
		}
		var cs CachedSnapshot
		if err := json.Unmarshal(raw, &cs); err != nil {
			continue
		}
		out = append(out, cs)
	}
	// Newest gen first — degraded birth wants the freshest available.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Gen > out[i].Gen {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out, nil
}

// FetchSnapshotFromDaemon is the Phase 1 stub. Phase 2 replaces this
// with a real HTTP fetch + sha verify + cache write-through. Until
// then it just logs and returns ErrDaemonUnavailable so birth knows
// to take the greenfield path.
func FetchSnapshotFromDaemon(cfg *config.Agent, logger *slog.Logger) (*CachedSnapshot, error) {
	if cfg == nil {
		return nil, errors.New("brain: FetchSnapshotFromDaemon requires a non-nil Agent")
	}
	if logger == nil {
		logger = slog.Default()
	}
	logger.Warn(
		"phantom-brain: FetchSnapshotFromDaemon is a no-op until Phase 2 daemon",
		slog.String("daemon", cfg.API),
	)
	return nil, ErrDaemonUnavailable
}
