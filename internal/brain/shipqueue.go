package brain

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"github.com/neverprepared/mcp-phantom-brain/internal/config"
)

// ShipQueueItem describes one death payload waiting for the daemon to
// pick it up. Exposed to operator tooling (brain_status MCP tool +
// future pbrainctl `queue depth` subcommand) so the size of the
// pending backlog is observable.
type ShipQueueItem struct {
	BrainID     string // owning brain (the parent dir of the payload)
	PayloadPath string
	SizeBytes   int64
}

// ListShipQueue enumerates every death-*.tar under ShipPendingDir()
// across all brain subdirectories. Sorted by path for stable output.
// Returns an empty slice (not error) when the dir doesn't exist —
// that's the steady state for a freshly initialised host.
func ListShipQueue(cfg *config.Agent) ([]ShipQueueItem, error) {
	if cfg == nil {
		return nil, errors.New("brain: ListShipQueue requires a non-nil Agent")
	}
	root := cfg.ShipPendingDir()
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("brain: read ship-pending: %w", err)
	}
	var out []ShipQueueItem
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		brainID := e.Name()
		sub := filepath.Join(root, brainID)
		files, err := os.ReadDir(sub)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			full := filepath.Join(sub, f.Name())
			st, err := os.Stat(full)
			if err != nil {
				continue
			}
			out = append(out, ShipQueueItem{
				BrainID:     brainID,
				PayloadPath: full,
				SizeBytes:   st.Size(),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PayloadPath < out[j].PayloadPath })
	return out, nil
}

// ShipQueueDepthBytes sums the sizes of every pending payload.
// Compared against CL_BRAIN_MAX_PENDING_MB to decide whether the brain
// should refuse new ingests (back-pressure). Phase 1 doesn't enforce
// the cap yet; this helper makes the value available to operators
// inspecting brain_status.
func ShipQueueDepthBytes(cfg *config.Agent) (int64, error) {
	items, err := ListShipQueue(cfg)
	if err != nil {
		return 0, err
	}
	var sum int64
	for _, it := range items {
		sum += it.SizeBytes
	}
	return sum, nil
}

// UploadShipQueue is the Phase 1 no-op. Phase 2 replaces this with a
// multipart MinIO + POST /api/brain/merge/init dance. Until then it
// logs the depth so operators see how much they're sitting on.
func UploadShipQueue(cfg *config.Agent, logger *slog.Logger) error {
	if cfg == nil {
		return errors.New("brain: UploadShipQueue requires a non-nil Agent")
	}
	if logger == nil {
		logger = slog.Default()
	}
	items, err := ListShipQueue(cfg)
	if err != nil {
		return err
	}
	logger.Warn(
		"phantom-brain: UploadShipQueue is a no-op until Phase 2 daemon",
		slog.Int("pending_count", len(items)),
	)
	return ErrDaemonUnavailable
}
