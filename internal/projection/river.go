package projection

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/neverprepared/phantom-brain/internal/pgstore/pgdb"
)

// defaultMaxWorkers caps concurrent projection jobs on the default queue.
// Modest by design — projection is I/O to the search store, not CPU-bound, and
// the SoR is the bottleneck of record.
const defaultMaxWorkers = 5

// MigrateRiver runs River's own schema migration (the river_job table and
// friends) Up against pool. River's tables live alongside the SoR tables in
// each per-profile database (pb_<profile>), so this is called once per profile
// DB. It is idempotent: a no-op when already at the latest version.
func MigrateRiver(ctx context.Context, pool *pgxpool.Pool) error {
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return fmt.Errorf("projection: init river migrator: %w", err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("projection: river migrate up: %w", err)
	}
	return nil
}

// NewWorkers builds the River worker registry for this package, registering
// ProjectRecordWorker against q (SoR access) and proj (projection target).
func NewWorkers(q *pgdb.Queries, proj Projector) *river.Workers {
	workers := river.NewWorkers()
	river.AddWorker(workers, NewProjectRecordWorker(q, proj))
	return workers
}

// NewClient constructs a River client over pool with the default queue running
// at a modest worker count. The caller owns the lifecycle: client.Start(ctx)
// to begin draining, client.Stop(ctx) to shut down gracefully.
//
// Pass workers from NewWorkers. The same pool backs both job storage and the
// driver, so InsertTx can enqueue inside an SoR transaction (the outbox).
func NewClient(pool *pgxpool.Pool, workers *river.Workers) (*river.Client[pgx.Tx], error) {
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: defaultMaxWorkers},
		},
		Workers: workers,
	})
	if err != nil {
		return nil, fmt.Errorf("projection: new river client: %w", err)
	}
	return client, nil
}

// EnqueueProjectTx is THE transactional-outbox primitive. It inserts a
// project_record (upsert) job on the SAME pgx.Tx the caller used to write the
// record. Because River's job insert participates in that transaction:
//
//   - the tx COMMITS  ⇒ the job is durably enqueued and will be worked;
//   - the tx ROLLS BACK ⇒ no job exists, nothing is projected.
//
// This is what lets a caller atomically write a record AND schedule its
// projection without dual-write divergence: there is no window where the
// record exists but the projection job does not (or vice-versa). River will
// not start the job until the transaction commits (snapshot visibility).
func EnqueueProjectTx(ctx context.Context, client *river.Client[pgx.Tx], tx pgx.Tx, recordID int64) error {
	_, err := client.InsertTx(ctx, tx, ProjectRecordArgs{
		RecordID: recordID,
		Op:       OpUpsert,
	}, nil)
	if err != nil {
		return fmt.Errorf("projection: enqueue project job for record %d: %w", recordID, err)
	}
	return nil
}

// EnqueueDeleteTx is the delete sibling of EnqueueProjectTx: it schedules
// removal of the projection for (profile, vault, sha), carrying the identity
// inline because the SoR record may already be gone by the time the job runs.
// Same transactional guarantee — commit enqueues, rollback does not.
func EnqueueDeleteTx(ctx context.Context, client *river.Client[pgx.Tx], tx pgx.Tx, profile, vault, sha string) error {
	_, err := client.InsertTx(ctx, tx, ProjectRecordArgs{
		Op:      OpDelete,
		Profile: profile,
		Vault:   vault,
		Sha:     sha,
	}, nil)
	if err != nil {
		return fmt.Errorf("projection: enqueue delete job for %s/%s/%s: %w", profile, vault, sha, err)
	}
	return nil
}

// WriteRecordAndEnqueue is the canonical "write + outbox" path the synth/ingest
// layer calls. In a single transaction it upserts the record and — on a fresh
// insert — enqueues its projection job, then commits. Any error rolls the whole
// thing back (so neither the record nor the job lands).
//
// Dedup handling: UpsertRecord uses ON CONFLICT (profile, vault, sha) DO
// NOTHING, which returns pgx.ErrNoRows when the content already exists. In that
// case the existing record is fetched and returned, and NO projection job is
// enqueued. Re-projecting is harmless (projection is idempotent), but skipping
// avoids needless churn — the existing record was already projected when first
// written. Callers that want to force a re-projection of an existing record
// should call EnqueueProjectTx directly.
func WriteRecordAndEnqueue(
	ctx context.Context,
	pool *pgxpool.Pool,
	client *river.Client[pgx.Tx],
	params pgdb.UpsertRecordParams,
) (rec pgdb.Record, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return pgdb.Record{}, fmt.Errorf("projection: begin tx: %w", err)
	}
	// Rollback is a no-op after a successful Commit; this guarantees rollback
	// on every early return / panic path.
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()

	q := pgdb.New(tx)

	rec, err = q.UpsertRecord(ctx, params)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Dedup: the (profile, vault, sha) already exists. Fetch it and
			// skip enqueueing — already projected on first write.
			existing, getErr := q.GetRecordBySHA(ctx, pgdb.GetRecordBySHAParams{
				Profile: params.Profile,
				Vault:   params.Vault,
				Sha:     params.Sha,
			})
			if getErr != nil {
				err = fmt.Errorf("projection: fetch existing record after dedup: %w", getErr)
				return pgdb.Record{}, err
			}
			if err = tx.Commit(ctx); err != nil {
				err = fmt.Errorf("projection: commit (dedup path): %w", err)
				return pgdb.Record{}, err
			}
			return existing, nil
		}
		err = fmt.Errorf("projection: upsert record: %w", err)
		return pgdb.Record{}, err
	}

	// Fresh insert: enqueue the projection job in the same tx (the outbox).
	if err = EnqueueProjectTx(ctx, client, tx, rec.ID); err != nil {
		return pgdb.Record{}, err
	}

	if err = tx.Commit(ctx); err != nil {
		err = fmt.Errorf("projection: commit: %w", err)
		return pgdb.Record{}, err
	}
	return rec, nil
}
