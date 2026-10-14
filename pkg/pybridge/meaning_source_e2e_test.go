//go:build e2e

// Real-Python end-to-end tests for the Issue #8 meaning-provenance
// contracts (ADR-0005: 来源 = cards.meaning_source 单列，material/generated，
// 编辑不改来源，空释义永不做标记): every scenario runs the REAL engine
// (vocab_manager.py in the shared venv, per-test temp DB) the way the
// parent photo-import commit drives it, and proves the durable business
// results:
//
//   - TestE2E_GeneratedMeaningStoredAndRidesSelection — AC-2/AC-4: a
//     supplemented meaning lands in cards with its provenance and the
//     final confirmed meaning rides due selection (the classroom reads
//     cards.meaning; the marker is parent-side only and never reaches
//     the selection payload).
//   - TestE2E_EditKeepsSource — AC-4: the ordinary edit path (the
//     parent's post-generation correction) changes the meaning WITHOUT
//     flipping the source — a corrected supplement stays a supplement,
//     a material meaning stays material.
//   - TestE2E_EmptyMeaningNeverMarked — AC-5: 保留缺失 (a cancelled or
//     never-answered supplement) stores an empty meaning with source
//     'material' — no marker without text, ever.
//   - TestE2E_LegacyDBMigratesMeaningSource — ADR-0005 Consequences: a
//     pre-#8 database (no meaning_source column) opens, migrates on the
//     first Python command, legacy rows read 'material', and new
//     generated rows mark normally.
package pybridge

import (
	"context"
	"testing"
)

// e2eMeaningSources reads word → (meaning, meaning_source) straight from
// the temp DB.
func e2eMeaningSources(t *testing.T, dbPath string) map[string][2]string {
	t.Helper()
	db := e2eOpenDB(t, dbPath)
	defer db.Close()
	rows, err := db.Query("SELECT word, meaning, meaning_source FROM cards")
	if err != nil {
		t.Fatalf("query cards: %v", err)
	}
	defer rows.Close()
	out := map[string][2]string{}
	for rows.Next() {
		var word, meaning, source string
		if err := rows.Scan(&word, &meaning, &source); err != nil {
			t.Fatalf("scan card: %v", err)
		}
		out[word] = [2]string{meaning, source}
	}
	return out
}

// TestE2E_GeneratedMeaningStoredAndRidesSelection proves the AC-2/AC-4
// engine truth end-to-end: provenance persists on the real write path and
// the classroom's word source (cmd_due) carries the FINAL confirmed
// meaning — there is no source dimension downstream, by design.
func TestE2E_GeneratedMeaningStoredAndRidesSelection(t *testing.T) {
	ctx := context.Background()
	b, _, cfg := e2eBridge(t, nil)

	add, err := b.RunAdd(ctx, AddOpts{Category: "english", Unit: "meaning-e2e", Entries: []AddEntry{
		{Word: "genapple", Meaning: "n. 苹果（补充）", MeaningSource: "generated"},
		{Word: "matbanana", Meaning: "香蕉"},
	}})
	if err != nil || add.Added != 2 {
		t.Fatalf("RunAdd = %+v, %v; want 2 added", add, err)
	}

	sources := e2eMeaningSources(t, cfg.VocabDBPath)
	if got := sources["genapple"]; got != [2]string{"n. 苹果（补充）", "generated"} {
		t.Fatalf("genapple stored %v, want meaning + generated", got)
	}
	if got := sources["matbanana"]; got != [2]string{"香蕉", "material"} {
		t.Fatalf("matbanana stored %v, want meaning + material", got)
	}

	due, err := b.RunDue(ctx, DueOpts{Category: "english", Peek: true, Units: []string{"meaning-e2e"}})
	if err != nil {
		t.Fatalf("RunDue: %v", err)
	}
	for _, c := range due.Cards {
		switch c.Word {
		case "genapple":
			if c.Meaning != "n. 苹果（补充）" {
				t.Errorf("due meaning for genapple = %q, want the confirmed supplement", c.Meaning)
			}
		case "matbanana":
			if c.Meaning != "香蕉" {
				t.Errorf("due meaning for matbanana = %q, want the material original", c.Meaning)
			}
		}
	}
}

// TestE2E_EditKeepsSource proves AC-4's engine half: cmd_edit (the
// ordinary PUT /words/{id} the parent's post-generation correction and
// the browse modal both ride) never touches meaning_source.
func TestE2E_EditKeepsSource(t *testing.T) {
	ctx := context.Background()
	b, _, cfg := e2eBridge(t, nil)

	if _, err := b.RunAdd(ctx, AddOpts{Category: "english", Entries: []AddEntry{
		{Word: "editgen", Meaning: "生成释义", MeaningSource: "generated"},
		{Word: "editmat", Meaning: "原文释义"},
	}}); err != nil {
		t.Fatalf("RunAdd: %v", err)
	}
	ids := reimportCardIDs(t, cfg.VocabDBPath)

	corrected := "家长改写的释义"
	if _, err := b.RunEdit(ctx, EditOpts{ID: ids["editgen"], Meaning: &corrected}); err != nil {
		t.Fatalf("edit generated: %v", err)
	}
	fixed := "修正后的原文释义"
	if _, err := b.RunEdit(ctx, EditOpts{ID: ids["editmat"], Meaning: &fixed}); err != nil {
		t.Fatalf("edit material: %v", err)
	}

	sources := e2eMeaningSources(t, cfg.VocabDBPath)
	if got := sources["editgen"]; got != [2]string{corrected, "generated"} {
		t.Fatalf("edited supplement = %v, want new meaning KEEPING generated (不冒充教材原文)", got)
	}
	if got := sources["editmat"]; got != [2]string{fixed, "material"} {
		t.Fatalf("edited material = %v, want new meaning KEEPING material", got)
	}
}

// TestE2E_EmptyMeaningNeverMarked proves the storage normalization behind
// AC-5's 保留缺失: an empty gloss never carries the marker, whatever the
// wire claimed (the cancel path and a never-answered row store the same
// shape).
func TestE2E_EmptyMeaningNeverMarked(t *testing.T) {
	ctx := context.Background()
	b, _, cfg := e2eBridge(t, nil)

	if _, err := b.RunAdd(ctx, AddOpts{Category: "english", Entries: []AddEntry{
		{Word: "keptmissing", Meaning: "", MeaningSource: "generated"},
	}}); err != nil {
		t.Fatalf("RunAdd: %v", err)
	}

	if got := e2eMeaningSources(t, cfg.VocabDBPath)["keptmissing"]; got != [2]string{"", "material"} {
		t.Fatalf("kept-missing row = %v, want empty meaning + material (空释义永不做标记)", got)
	}
}

// TestE2E_LegacyDBMigratesMeaningSource proves the zero-breakage
// migration: a database whose cards table predates Issue #8 (no
// meaning_source column) is migrated by the first Python command, every
// legacy row reads 'material', and a fresh generated row marks normally.
func TestE2E_LegacyDBMigratesMeaningSource(t *testing.T) {
	ctx := context.Background()
	b, _, cfg := e2eBridge(t, nil)

	// Build the pre-#8 shape by hand, then seed one legacy row through the
	// OLD column list (init_db's CREATE IF NOT EXISTS leaves it alone).
	db := e2eOpenDB(t, cfg.VocabDBPath)
	if _, err := db.Exec(`CREATE TABLE cards (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		word TEXT NOT NULL,
		meaning TEXT NOT NULL DEFAULT '',
		example TEXT NOT NULL DEFAULT '',
		unit TEXT NOT NULL DEFAULT '',
		category TEXT NOT NULL DEFAULT 'english',
		fsrs_card TEXT NOT NULL DEFAULT '',
		participates INTEGER NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		UNIQUE(word, category)
	)`); err != nil {
		t.Fatalf("create legacy cards: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO cards (word, meaning, fsrs_card) VALUES ('legacyword', '旧释义', '{}')`); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	db.Close()

	// Any bridge command runs the real script → init_db migrates.
	if _, err := b.RunAdd(ctx, AddOpts{Category: "english", Entries: []AddEntry{
		{Word: "newgen", Meaning: "新补充", MeaningSource: "generated"},
	}}); err != nil {
		t.Fatalf("RunAdd on legacy DB: %v", err)
	}

	sources := e2eMeaningSources(t, cfg.VocabDBPath)
	if got := sources["legacyword"]; got != [2]string{"旧释义", "material"} {
		t.Fatalf("legacy row = %v, want untouched meaning + material default", got)
	}
	if got := sources["newgen"]; got != [2]string{"新补充", "generated"} {
		t.Fatalf("new row = %v, want generated marker post-migration", got)
	}
}
