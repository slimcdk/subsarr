package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slimcdk/subsarr/internal/database"
)

// Bazarr's two query shapes have to be index seeks. A plan that scans `uploads`
// is the ten-to-thirty-second search this whole change exists to remove, and it
// is the kind of regression that no functional test would notice — the results
// would still be right.

func explainDB(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()

	db, err := database.Open("sqlite", filepath.Join(t.TempDir(), "explain.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	migrator, err := database.NewMigrator(db, "sqlite")
	if err != nil {
		t.Fatalf("migrator: %v", err)
	}
	if err := migrator.Up(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	st := &sqlStore{db: db, d: sqliteDialect{}}
	uploads := make([]Upload, 0, 200)
	files := make([]IngestFile, 0, 200)
	for n := range 200 {
		id := fmt.Sprintf("%d", 1000+n)
		uploads = append(uploads, Upload{
			ID: id, SubsceneID: id, Slug: fmt.Sprintf("film-%d", n%40),
			Title: fmt.Sprintf("Film %d", n%40), ImdbID: fmt.Sprintf("tt%07d", n%40),
			Language: []string{"english", "danish", "thai"}[n%3], Releases: "[]",
		})
		files = append(files, IngestFile{
			ID: "f" + id, UploadID: id, Filename: "a.srt", Format: "srt",
			ContentHash: fmt.Sprintf("%064d", n), ContentKey: "content/a",
		})
	}
	if err := st.UpsertUploads(ctx, uploads); err != nil {
		t.Fatal(err)
	}
	if err := st.IngestFiles(ctx, files); err != nil {
		t.Fatal(err)
	}
	if err := st.Reindex(ctx); err != nil {
		t.Fatal(err)
	}
	// Without statistics the planner cannot tell the composite index from the
	// single-column one, which is exactly what Optimize is for.
	if err := st.Optimize(ctx); err != nil {
		t.Fatal(err)
	}
	return db
}

func explain(t *testing.T, db *sql.DB, b *builder) string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+b.String(), b.args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan.WriteString(detail)
		plan.WriteString("\n")
	}
	return plan.String()
}

func TestPlan_IMDBSearchUsesTheCompositeIndex(t *testing.T) {
	db := explainDB(t)
	st := &sqlStore{db: db, d: sqliteDialect{}}

	b, _ := st.buildSearch(SearchParams{ImdbID: "tt0000007", Language: "english", Limit: 50}, nil, false)
	plan := explain(t, db, b)

	if !strings.Contains(plan, "idx_uploads_imdb_lang") {
		t.Errorf("the IMDB path does not use idx_uploads_imdb_lang:\n%s", plan)
	}
	if strings.Contains(plan, "SCAN uploads") {
		t.Errorf("the IMDB path scans uploads:\n%s", plan)
	}
}

func TestPlan_TitleSearchUsesTheTitleIndex(t *testing.T) {
	db := explainDB(t)
	st := &sqlStore{db: db, d: sqliteDialect{}}

	mode := matchAllWords
	b, ok := st.buildSearch(SearchParams{Query: "Film 7", Language: "english", Limit: 50}, &mode, false)
	if !ok {
		t.Fatal("the title path refused to build a query")
	}
	plan := explain(t, db, b)

	if !strings.Contains(plan, "titles_fts") {
		t.Errorf("the title path does not use the title index:\n%s", plan)
	}
	if !strings.Contains(plan, "idx_uploads_slug_lang") {
		t.Errorf("the title path does not use idx_uploads_slug_lang:\n%s", plan)
	}
	if strings.Contains(plan, "SCAN uploads") {
		t.Errorf("the title path scans uploads:\n%s", plan)
	}
}
