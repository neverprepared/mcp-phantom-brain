//go:build integration

// Integration coverage for the hybrid recall read path (pb-hybrid search
// pipeline + Recaller) against a real OpenSearch via testcontainers.
// Build-tagged OFF by default so `make test` neither compiles this file
// nor needs Docker. Run with:
//
//	GOFLAGS="-tags=sqlite_fts5,integration" go test ./internal/osproject/ -run Integration -count=1 -v
package osproject

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	pgvector "github.com/pgvector/pgvector-go"
	tcopensearch "github.com/testcontainers/testcontainers-go/modules/opensearch"

	"github.com/neverprepared/phantom-brain/internal/osearch"
	"github.com/neverprepared/phantom-brain/internal/pgstore/pgdb"
)

// startOSForRecall spins up a single-node OpenSearch and returns a Client
// + a distinct test prefix (separate from the projector test's prefix so
// the two integration suites never collide on a shared cluster).
func startOSForRecall(t *testing.T) (*osearch.Client, string) {
	t.Helper()
	ctx := context.Background()

	ctr, err := tcopensearch.Run(ctx, testImage)
	if err != nil {
		t.Fatalf("start opensearch container: %v", err)
	}
	t.Cleanup(func() {
		if err := ctr.Terminate(context.Background()); err != nil {
			t.Logf("terminate container: %v", err)
		}
	})

	addr, err := ctr.Address(ctx)
	if err != nil {
		t.Fatalf("container address: %v", err)
	}

	cfg := osearch.DefaultConfig()
	cfg.Addresses = []string{addr}
	cfg.RequestTimeout = 15 * time.Second
	cfg.Username = ctr.User
	cfg.Password = ctr.Password

	openCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	c, err := osearch.Open(openCtx, cfg)
	if err != nil {
		t.Fatalf("osearch.Open: %v", err)
	}
	return c, "pbrecall_test_"
}

// vecToward builds a 768-dim vector dominated by a single hot dimension,
// so two records with different hot dims are far apart and a query with
// the same hot dim retrieves its match unambiguously under cosine.
func vecToward(hotDim int) []float32 {
	v := make([]float32, osearch.EmbeddingDim)
	for i := range v {
		v[i] = 0.001
	}
	v[hotDim] = 1.0
	return v
}

func recRecord(id int64, profile, vault, sha, kind, topic, title, body string, emb []float32) pgdb.Record {
	now := time.Now().UTC().Truncate(time.Second)
	rec := pgdb.Record{
		ID:        id,
		Profile:   profile,
		Vault:     vault,
		Sha:       sha,
		Kind:      kind,
		Topic:     pgtype.Text{String: topic, Valid: topic != ""},
		Title:     title,
		Body:      pgtype.Text{String: body, Valid: true},
		CreatedAt: pgtype.Timestamptz{Time: now, Valid: true},
		UpdatedAt: pgtype.Timestamptz{Time: now, Valid: true},
	}
	if emb != nil {
		ev := pgvector.NewVector(emb)
		rec.Embedding = &ev
	}
	return rec
}

func TestRecallIntegration(t *testing.T) {
	c, prefix := startOSForRecall(t)
	ctx := context.Background()
	proj := NewWithRefresh(c, prefix)
	rec := NewRecaller(c, prefix)

	if err := EnsureIndex(ctx, c, prefix); err != nil {
		t.Fatalf("EnsureIndex (1st): %v", err)
	}
	if err := EnsureIndex(ctx, c, prefix); err != nil {
		t.Fatalf("EnsureIndex (2nd, idempotent): %v", err)
	}
	if err := EnsureSearchPipeline(ctx, c); err != nil {
		t.Fatalf("EnsureSearchPipeline (1st): %v", err)
	}
	if err := EnsureSearchPipeline(ctx, c); err != nil {
		t.Fatalf("EnsureSearchPipeline (2nd, idempotent): %v", err)
	}

	// Tenant: tctest/main. Hot dims chosen so the "ledgers" doc is the
	// clear kNN winner for a query vector aimed at dim 5, while the
	// "invoices" doc is the BM25 winner for the text "invoice".
	const (
		dimLedger  = 5
		dimUnrel   = 300
		dimReceipt = 100
	)
	records := []pgdb.Record{
		// Lexical target: text "invoice" should hit this (english stems
		// invoices -> invoice). Kind note. No embedding -> proves recall
		// still surfaces embedding-less docs lexically.
		recRecord(1, "tctest", "main", "sha-invoices", "note", "knowledge",
			"Quarterly invoices",
			"We reconciled every quarterly invoice against the vendor statements.",
			nil),
		// Semantic target: nearest to a query vector aimed at dimLedger.
		// Its TEXT is deliberately unrelated to "invoice" so when it
		// surfaces it is via kNN reach, not BM25.
		recRecord(2, "tctest", "main", "sha-ledgers", "note", "memory",
			"General ledgers and cosmic bookkeeping",
			"Stardust accounting principles for interstellar treasuries.",
			vecToward(dimLedger)),
		// A web_scrape doc to prove the Kinds=[note] filter excludes it.
		recRecord(3, "tctest", "main", "sha-webscrape", "web_scrape", "tools",
			"Scraped invoice tutorial",
			"A web page describing how to file an invoice online.",
			vecToward(dimReceipt)),
		// Unrelated doc, far hot dim, different topic.
		recRecord(4, "tctest", "main", "sha-unrelated", "note", "agents",
			"Gardening almanac",
			"Companion planting schedules for tomatoes and basil.",
			vecToward(dimUnrel)),
		// OTHER tenant — must never appear in tctest/main recall, even
		// though its text matches "invoice" exactly.
		recRecord(5, "othertenant", "main", "sha-other-invoice", "note", "knowledge",
			"Other tenant invoices",
			"This invoice belongs to a different tenant entirely.",
			vecToward(dimLedger)),
	}
	for _, rc := range records {
		if err := proj.Project(ctx, rc); err != nil {
			t.Fatalf("project %s: %v", rc.Sha, err)
		}
	}

	tenantSHAs := func(hits []RecallHit) map[string]RecallHit {
		m := map[string]RecallHit{}
		for _, h := range hits {
			m[h.SHA] = h
		}
		return m
	}

	t.Run("Hybrid_SemanticAndLexicalReach", func(t *testing.T) {
		// Text matches the invoices doc (BM25 reach); Vector aimed at the
		// ledgers doc (kNN reach). Fused result should surface BOTH.
		hits, err := rec.Recall(ctx, RecallQuery{
			Profile: "tctest", Vault: "main",
			Text:   "invoice",
			Vector: vecToward(dimLedger),
			Size:   10,
		})
		if err != nil {
			t.Fatalf("Recall: %v", err)
		}
		if len(hits) == 0 {
			t.Fatal("hybrid recall returned no hits")
		}
		got := tenantSHAs(hits)
		if _, ok := got["sha-invoices"]; !ok {
			t.Errorf("expected lexical match sha-invoices in hybrid result; got %v", shaList(hits))
		}
		if _, ok := got["sha-ledgers"]; !ok {
			t.Errorf("expected semantic match sha-ledgers in hybrid result; got %v", shaList(hits))
		}
		// Tenant isolation: the other-tenant invoice must never appear.
		if _, ok := got["sha-other-invoice"]; ok {
			t.Errorf("tenant isolation breach: sha-other-invoice surfaced in tctest/main recall")
		}
		// Ranked: scores must be non-increasing (response order is fused).
		for i := 1; i < len(hits); i++ {
			if hits[i].Score > hits[i-1].Score {
				t.Errorf("hits not in non-increasing score order at %d: %v > %v", i, hits[i].Score, hits[i-1].Score)
			}
		}
	})

	t.Run("Degraded_BM25Only", func(t *testing.T) {
		hits, err := rec.Recall(ctx, RecallQuery{
			Profile: "tctest", Vault: "main",
			Text: "invoice",
			// Vector nil -> no pipeline, plain bool query.
			Size: 10,
		})
		if err != nil {
			t.Fatalf("Recall (degraded): %v", err)
		}
		got := tenantSHAs(hits)
		if _, ok := got["sha-invoices"]; !ok {
			t.Errorf("degraded BM25 should surface sha-invoices; got %v", shaList(hits))
		}
		if _, ok := got["sha-other-invoice"]; ok {
			t.Errorf("tenant isolation breach in degraded mode: sha-other-invoice surfaced")
		}
	})

	t.Run("EnglishStemmingThroughRecall", func(t *testing.T) {
		// Query "invoice" (singular) must find the doc bodied/titled
		// "invoices" (plural) via the english analyzer.
		hits, err := rec.Recall(ctx, RecallQuery{
			Profile: "tctest", Vault: "main",
			Text: "invoice",
			Size: 10,
		})
		if err != nil {
			t.Fatalf("Recall: %v", err)
		}
		if _, ok := tenantSHAs(hits)["sha-invoices"]; !ok {
			t.Errorf("stemming: 'invoice' should match 'invoices'; got %v", shaList(hits))
		}
	})

	t.Run("Filter_KindsExcludesWebScrape", func(t *testing.T) {
		// "invoice" matches both sha-invoices (note) and sha-webscrape
		// (web_scrape). Kinds=[note] must drop the web_scrape.
		hits, err := rec.Recall(ctx, RecallQuery{
			Profile: "tctest", Vault: "main",
			Text:  "invoice",
			Kinds: []string{"note"},
			Size:  10,
		})
		if err != nil {
			t.Fatalf("Recall: %v", err)
		}
		got := tenantSHAs(hits)
		if _, ok := got["sha-invoices"]; !ok {
			t.Errorf("kind filter dropped the note it should keep; got %v", shaList(hits))
		}
		if _, ok := got["sha-webscrape"]; ok {
			t.Errorf("Kinds=[note] should exclude the web_scrape doc; got %v", shaList(hits))
		}
	})

	t.Run("Snippet_NonEmpty", func(t *testing.T) {
		hits, err := rec.Recall(ctx, RecallQuery{
			Profile: "tctest", Vault: "main",
			Text: "invoice",
			Size: 10,
		})
		if err != nil {
			t.Fatalf("Recall: %v", err)
		}
		if len(hits) == 0 {
			t.Fatal("no hits to check snippet on")
		}
		for _, h := range hits {
			if h.Snippet == "" {
				t.Errorf("hit %s carried an empty snippet", h.SHA)
			}
		}
	})

	t.Run("Validation_EmptyProfile", func(t *testing.T) {
		if _, err := rec.Recall(ctx, RecallQuery{Vault: "main", Text: "x"}); err == nil {
			t.Fatal("expected error for empty Profile")
		}
		if _, err := rec.Recall(ctx, RecallQuery{Profile: "tctest", Vault: "main"}); err == nil {
			t.Fatal("expected error when neither Text nor Vector is present")
		}
	})
}

func shaList(hits []RecallHit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.SHA)
	}
	return out
}
