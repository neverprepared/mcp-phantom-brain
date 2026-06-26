// Package projection implements the transactional outbox + River projection
// worker for phantom-brain (design §13.1).
//
// Postgres is the System of Record (SoR); OpenSearch is a DERIVED projection.
// We do not dual-write (write PG, then write OS separately — they diverge on
// partial failure). Instead, River's transactionally-inserted job IS the
// outbox: when a record is written to PG, the SAME pgx.Tx inserts a River job
// to project that record. Commit ⇒ the job is durably enqueued; rollback ⇒ no
// job exists. River then drains the job (with retries, backoff, durability)
// and applies it to the projection target.
//
// This package delivers the plumbing — the job, the worker, the Projector
// interface, and the transactional-enqueue primitive. The real OpenSearch
// Projector is a follow-up layer; here the default is LogProjector.
package projection

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/neverprepared/phantom-brain/internal/pgstore/pgdb"
)

// Projector is the pluggable projection target. The real OpenSearch
// implementation is a later layer; the plumbing here only needs the interface
// plus a test fake and the LogProjector no-op default.
//
// Idempotency contract: Project MUST be an upsert keyed on
// (profile, vault, sha). Projecting the same record twice — or projecting a
// record that was already projected — must be safe and converge to the same
// state. The projection worker relies on this: River guarantees at-least-once
// delivery, so a job may be worked more than once (retry after a crash between
// projection success and job-row deletion), and the worker must tolerate it.
type Projector interface {
	// Project upserts the record into the projection target, keyed on
	// (profile, vault, sha). Returning a non-nil error causes River to retry
	// the job.
	Project(ctx context.Context, rec pgdb.Record) error

	// DeleteProjection removes the projection identified by (profile, vault,
	// sha). It must be idempotent — deleting an absent projection is not an
	// error.
	DeleteProjection(ctx context.Context, profile, vault, sha string) error
}

// LogProjector is the default no-op-ish Projector: it logs the operation via
// the slog default logger and returns nil. The daemon wires a real (e.g.
// OpenSearch) Projector in a later layer; until then this keeps the pipeline
// runnable end-to-end.
type LogProjector struct{}

// Project logs the upsert and returns nil.
func (LogProjector) Project(_ context.Context, rec pgdb.Record) error {
	slog.Info("projection: upsert",
		"record_id", rec.ID,
		"profile", rec.Profile,
		"vault", rec.Vault,
		"sha", rec.Sha,
		"kind", rec.Kind,
	)
	return nil
}

// DeleteProjection logs the delete and returns nil.
func (LogProjector) DeleteProjection(_ context.Context, profile, vault, sha string) error {
	slog.Info("projection: delete",
		"profile", profile,
		"vault", vault,
		"sha", sha,
	)
	return nil
}

// ProjectRecordArgs are the arguments for a single projection job. For an
// upsert only RecordID is needed — the worker loads the current record from
// the SoR. For a delete the record may already be gone, so the identity
// (Profile, Vault, Sha) is carried inline.
type ProjectRecordArgs struct {
	RecordID int64  `json:"record_id"`
	Op       string `json:"op"` // "upsert" | "delete"

	// Delete-only identity. Omitted for upserts (RecordID is enough there).
	Profile string `json:"profile,omitempty"`
	Vault   string `json:"vault,omitempty"`
	Sha     string `json:"sha,omitempty"`
}

// Op values.
const (
	OpUpsert = "upsert"
	OpDelete = "delete"
)

// Kind identifies the job type for River. Stable string — do not rename once
// jobs of this kind exist in a non-empty database.
func (ProjectRecordArgs) Kind() string { return "project_record" }

// ProjectRecordWorker drains project_record jobs: it loads the record from the
// SoR (for upserts) and applies the op to the Projector.
type ProjectRecordWorker struct {
	river.WorkerDefaults[ProjectRecordArgs]
	q    *pgdb.Queries
	proj Projector
}

// NewProjectRecordWorker constructs the worker bound to a query set and a
// projection target.
func NewProjectRecordWorker(q *pgdb.Queries, proj Projector) *ProjectRecordWorker {
	return &ProjectRecordWorker{q: q, proj: proj}
}

// Work applies one projection job. It is idempotent: re-delivery of the same
// job converges to the same projection state (see the Projector contract).
func (w *ProjectRecordWorker) Work(ctx context.Context, job *river.Job[ProjectRecordArgs]) error {
	switch job.Args.Op {
	case OpDelete:
		if err := w.proj.DeleteProjection(ctx, job.Args.Profile, job.Args.Vault, job.Args.Sha); err != nil {
			return fmt.Errorf("projection: delete projection for %s/%s/%s: %w",
				job.Args.Profile, job.Args.Vault, job.Args.Sha, err)
		}
		return nil

	case OpUpsert, "": // default empty op to upsert for forward-compat.
		rec, err := w.q.GetRecordByID(ctx, job.Args.RecordID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// The record was deleted before this projection ran. Nothing
				// to upsert — not an error. A subsequent delete job (if any)
				// handles removal; an orphaned upsert is simply a no-op.
				slog.Info("projection: record gone, skipping upsert",
					"record_id", job.Args.RecordID)
				return nil
			}
			// Other errors are transient (connection, timeout) — let River
			// retry by returning the error.
			return fmt.Errorf("projection: load record %d: %w", job.Args.RecordID, err)
		}
		if err := w.proj.Project(ctx, rec); err != nil {
			return fmt.Errorf("projection: project record %d (%s/%s/%s): %w",
				rec.ID, rec.Profile, rec.Vault, rec.Sha, err)
		}
		return nil

	default:
		// Unknown op: don't retry forever on a poison job — fail it once so it
		// surfaces, but River will exhaust attempts and discard it.
		return fmt.Errorf("projection: unknown op %q for record %d", job.Args.Op, job.Args.RecordID)
	}
}
