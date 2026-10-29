//go:build e2e

// Real-Python E2E for Issue #80 U10 atomic designation create
// (RunCreateDesignation): selection, locks and pending snapshot in one
// BEGIN IMMEDIATE transaction; request-key idempotency and failure rollback.
package pybridge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

func dcMigrateDesignations(t *testing.T, dbPath string) {
	t.Helper()
	db, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := lesson.Migrate(context.Background(), db); err != nil {
		t.Fatalf("lesson.Migrate: %v", err)
	}
}

func dcDesignationCount(t *testing.T, dbPath string) int {
	t.Helper()
	db := e2eOpenDB(t, dbPath)
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM designations").Scan(&n); err != nil {
		t.Fatalf("count designations: %v", err)
	}
	return n
}

func dcCreateOpts(bookID int64, lesson, key string, dbPath string) CreateDesignationOpts {
	return CreateDesignationOpts{
		DesignateOpts:  DesignateOpts{TextbookID: bookID, Lesson: lesson},
		RequestKey:     key,
		Category:       "english",
		GoDatabasePath: dbPath,
	}
}

func dcUnlockCards(t *testing.T, b *Bridge, ctx context.Context, ids []int64) {
	t.Helper()
	if _, err := b.RunUnlock(ctx, UnlockOpts{IDs: ids}); err != nil {
		t.Fatalf("RunUnlock: %v", err)
	}
}

func dcAssertAllUnlocked(t *testing.T, dbPath string, ids []int64) {
	t.Helper()
	locks := dgLocks(t, dbPath)
	for _, id := range ids {
		if locks[id] != "" {
			t.Fatalf("card %d still locked %q, want empty", id, locks[id])
		}
	}
}

// TestE2E_CreateDesignationAtomicOk — ok path writes one pending row and locks.
func TestE2E_CreateDesignationAtomicOk(t *testing.T) {
	b, _, cfg := e2eBridge(t, nil)
	dcMigrateDesignations(t, cfg.VocabDBPath)
	ctx := context.Background()

	bookID := dgSetupBook(t, b, "dc-ok", "第4单元")
	ids := dgAddWords(t, b, "dcok", 3)
	dgAssignLesson(t, b, bookID, "第4单元", ids)

	res, err := b.RunCreateDesignation(ctx, dcCreateOpts(bookID, "第4单元", "req-ok-1", cfg.VocabDBPath))
	if err != nil {
		t.Fatalf("RunCreateDesignation: %v", err)
	}
	if res.Status != DesignateStatusOK || res.DesignationID <= 0 ||
		res.DesignationStatus != "pending" || len(res.Cards) != 3 {
		t.Fatalf("result = %+v, want ok/pending/3 cards with designation id", res)
	}
	if dcDesignationCount(t, cfg.VocabDBPath) != 1 {
		t.Fatalf("designations count != 1 after ok create")
	}
	for _, id := range ids {
		locks := dgLocks(t, cfg.VocabDBPath)
		if locks[id] == "" {
			t.Fatalf("card %d not locked after atomic create", id)
		}
	}
}

// TestE2E_CreateDesignationIdempotentRetry — same request key returns the same row.
func TestE2E_CreateDesignationIdempotentRetry(t *testing.T) {
	b, _, cfg := e2eBridge(t, nil)
	dcMigrateDesignations(t, cfg.VocabDBPath)
	ctx := context.Background()

	bookID := dgSetupBook(t, b, "dc-retry", "第4单元")
	ids := dgAddWords(t, b, "dcret", 2)
	dgAssignLesson(t, b, bookID, "第4单元", ids)

	key := "req-retry-a"
	first, err := b.RunCreateDesignation(ctx, dcCreateOpts(bookID, "第4单元", key, cfg.VocabDBPath))
	if err != nil || first.DesignationID <= 0 {
		t.Fatalf("first create = %+v, %v", first, err)
	}
	dcUnlockCards(t, b, ctx, ids)

	second, err := b.RunCreateDesignation(ctx, dcCreateOpts(bookID, "第4单元", key, cfg.VocabDBPath))
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if second.DesignationID != first.DesignationID || second.DesignationStatus != "pending" {
		t.Fatalf("retry = %+v, want same id %d pending", second, first.DesignationID)
	}
	if dcDesignationCount(t, cfg.VocabDBPath) != 1 {
		t.Fatalf("retry created a second designation row")
	}
	dcAssertAllUnlocked(t, cfg.VocabDBPath, ids)
}

// TestE2E_CreateDesignationRetryAfterConsumed reads consumed status without re-locking.
func TestE2E_CreateDesignationRetryAfterConsumed(t *testing.T) {
	b, _, cfg := e2eBridge(t, nil)
	dcMigrateDesignations(t, cfg.VocabDBPath)
	ctx := context.Background()

	bookID := dgSetupBook(t, b, "dc-cons", "第4单元")
	ids := dgAddWords(t, b, "dccon", 2)
	dgAssignLesson(t, b, bookID, "第4单元", ids)
	key := "req-consumed"

	created, err := b.RunCreateDesignation(ctx, dcCreateOpts(bookID, "第4单元", key, cfg.VocabDBPath))
	if err != nil || created.DesignationID <= 0 {
		t.Fatalf("create: %+v, %v", created, err)
	}

	db, err := database.Open(cfg.VocabDBPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	store, err := lesson.NewStore(db, nil)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if ok, err := store.ConsumeDesignation(ctx, created.DesignationID); err != nil || !ok {
		t.Fatalf("ConsumeDesignation: ok=%v err=%v", ok, err)
	}
	dcUnlockCards(t, b, ctx, ids)

	retry, err := b.RunCreateDesignation(ctx, dcCreateOpts(bookID, "第4单元", key, cfg.VocabDBPath))
	if err != nil {
		t.Fatalf("retry after consumed: %v", err)
	}
	if retry.DesignationID != created.DesignationID || retry.DesignationStatus != "consumed" {
		t.Fatalf("retry = %+v, want same id consumed", retry)
	}
	dcAssertAllUnlocked(t, cfg.VocabDBPath, ids)
}

// TestE2E_CreateDesignationRetryAfterLessonRemoved — idempotent replay must
// not require the lesson to remain on the current textbook list.
func TestE2E_CreateDesignationRetryAfterLessonRemoved(t *testing.T) {
	b, _, cfg := e2eBridge(t, nil)
	dcMigrateDesignations(t, cfg.VocabDBPath)
	ctx := context.Background()

	bookID := dgSetupBook(t, b, "dc-lessrm", "第4单元")
	ids := dgAddWords(t, b, "dclrm", 2)
	dgAssignLesson(t, b, bookID, "第4单元", ids)
	key := "req-lesson-removed"

	first, err := b.RunCreateDesignation(ctx, dcCreateOpts(bookID, "第4单元", key, cfg.VocabDBPath))
	if err != nil || first.DesignationID <= 0 {
		t.Fatalf("first create: %+v, %v", first, err)
	}
	if _, err := b.RunTextbookUpdate(ctx, TextbookUpdateOpts{
		ID: bookID, Lessons: []string{"第1课"},
	}); err != nil {
		t.Fatalf("remove target lesson from list: %v", err)
	}

	retry, err := b.RunCreateDesignation(ctx, dcCreateOpts(bookID, "第4单元", key, cfg.VocabDBPath))
	if err != nil {
		t.Fatalf("retry after lesson removed from list: %v", err)
	}
	if retry.DesignationID != first.DesignationID || retry.DesignationStatus != "pending" {
		t.Fatalf("retry = %+v, want same pending designation", retry)
	}
}

// TestE2E_CreateDesignationKeyConflict — same key, different target.
func TestE2E_CreateDesignationKeyConflict(t *testing.T) {
	b, _, cfg := e2eBridge(t, nil)
	dcMigrateDesignations(t, cfg.VocabDBPath)
	ctx := context.Background()

	bookID := dgSetupBook(t, b, "dc-kc", "第4单元")
	ids := dgAddWords(t, b, "dckc", 2)
	dgAssignLesson(t, b, bookID, "第4单元", ids)
	key := "shared-key"

	if _, err := b.RunCreateDesignation(ctx, dcCreateOpts(bookID, "第4单元", key, cfg.VocabDBPath)); err != nil {
		t.Fatalf("first create: %v", err)
	}
	res, err := b.RunCreateDesignation(ctx, dcCreateOpts(bookID, "第1课", key, cfg.VocabDBPath))
	if err != nil {
		t.Fatalf("conflict call: %v", err)
	}
	if res.Status != DesignateStatusKeyConflict {
		t.Fatalf("status = %q, want key_conflict", res.Status)
	}
}

// TestE2E_CreateDesignationRejectsWithoutSnapshot — occupied / too_many / empty.
func TestE2E_CreateDesignationRejectsWithoutSnapshot(t *testing.T) {
	b, _, cfg := e2eBridge(t, nil)
	dcMigrateDesignations(t, cfg.VocabDBPath)
	ctx := context.Background()

	bookOver := dgSetupBook(t, b, "dc-tm", "第5单元")
	overIDs := dgAddWords(t, b, "dctm", 101)
	dgAssignLesson(t, b, bookOver, "第5单元", overIDs)
	res, err := b.RunCreateDesignation(ctx, dcCreateOpts(bookOver, "第5单元", "req-tm", cfg.VocabDBPath))
	if err != nil || res.Status != DesignateStatusTooMany {
		t.Fatalf("too_many = %+v, %v", res, err)
	}
	if dcDesignationCount(t, cfg.VocabDBPath) != 0 {
		t.Fatalf("too_many wrote a designation row")
	}

	bookOcc := dgSetupBook(t, b, "dc-oc", "第6单元")
	occIDs := dgAddWords(t, b, "dcoc", 3)
	dgAssignLesson(t, b, bookOcc, "第6单元", occIDs)
	if _, err := b.RunCreateDesignation(ctx, dcCreateOpts(bookOcc, "第6单元", "req-occ-first", cfg.VocabDBPath)); err != nil {
		t.Fatalf("first occupy lesson: %v", err)
	}
	res, err = b.RunCreateDesignation(ctx, dcCreateOpts(bookOcc, "第6单元", "req-occ-second", cfg.VocabDBPath))
	if err != nil || res.Status != DesignateStatusOccupied {
		t.Fatalf("occupied = %+v, %v", res, err)
	}
	if dcDesignationCount(t, cfg.VocabDBPath) != 1 {
		t.Fatalf("occupied path should leave exactly the first designation")
	}

	bookEmpty := dgSetupBook(t, b, "dc-empty", "第7单元")
	res, err = b.RunCreateDesignation(ctx, dcCreateOpts(bookEmpty, "第7单元", "req-empty", cfg.VocabDBPath))
	if err != nil || res.Status != DesignateStatusOK || len(res.Cards) != 0 || res.DesignationID != 0 {
		t.Fatalf("empty lesson = %+v, %v; want ok/0 cards/no designation id", res, err)
	}
	if dcDesignationCount(t, cfg.VocabDBPath) != 1 {
		t.Fatalf("empty target on a fresh book must not add a designation row")
	}
}

// TestE2E_CreateDesignationInsertFailureUnlocks — snapshot INSERT failure rolls back locks.
func TestE2E_CreateDesignationInsertFailureUnlocks(t *testing.T) {
	t.Setenv("HTA_TEST_FAIL_DESIGNATION_INSERT", "1")
	b, _, cfg := e2eBridge(t, nil)
	dcMigrateDesignations(t, cfg.VocabDBPath)
	ctx := context.Background()

	bookID := dgSetupBook(t, b, "dc-fail", "第4单元")
	ids := dgAddWords(t, b, "dcfail", 2)
	dgAssignLesson(t, b, bookID, "第4单元", ids)
	locksBefore := dgLocks(t, cfg.VocabDBPath)

	_, err := b.RunCreateDesignation(ctx, dcCreateOpts(bookID, "第4单元", "req-fail", cfg.VocabDBPath))
	if err == nil {
		t.Fatal("want subprocess failure when designation insert is injected")
	}
	if dcDesignationCount(t, cfg.VocabDBPath) != 0 {
		t.Fatalf("failed create must not leave a designation row")
	}
	locksAfter := dgLocks(t, cfg.VocabDBPath)
	for _, id := range ids {
		if locksAfter[id] != locksBefore[id] {
			t.Fatalf("card %d lock changed on failed snapshot insert: before %q after %q",
				id, locksBefore[id], locksAfter[id])
		}
	}
}

// TestE2E_CreateDesignationDatabaseSplit — mismatched Go/Python DB paths fail fast.
func TestE2E_CreateDesignationDatabaseSplit(t *testing.T) {
	b, _, cfg := e2eBridge(t, nil)
	dcMigrateDesignations(t, cfg.VocabDBPath)
	ctx := context.Background()

	bookID := dgSetupBook(t, b, "dc-split", "第4单元")
	ids := dgAddWords(t, b, "dcspl", 1)
	dgAssignLesson(t, b, bookID, "第4单元", ids)

	opts := dcCreateOpts(bookID, "第4单元", "req-split", "/tmp/other-vocab.db")
	_, err := b.RunCreateDesignation(ctx, opts)
	if !errors.Is(err, ErrDesignationDatabaseSplit) {
		t.Fatalf("error = %v, want ErrDesignationDatabaseSplit", err)
	}
}

// TestE2E_CreateDesignationRequiresGoDBPath — empty GoDatabasePath must not
// bypass the single-file guard.
func TestE2E_CreateDesignationRequiresGoDBPath(t *testing.T) {
	b, _, cfg := e2eBridge(t, nil)
	dcMigrateDesignations(t, cfg.VocabDBPath)
	ctx := context.Background()

	bookID := dgSetupBook(t, b, "dc-nopath", "第4单元")
	ids := dgAddWords(t, b, "dcnp", 1)
	dgAssignLesson(t, b, bookID, "第4单元", ids)

	opts := dcCreateOpts(bookID, "第4单元", "req-nopath", "")
	_, err := b.RunCreateDesignation(ctx, opts)
	if !errors.Is(err, ErrDesignationDatabasePathRequired) {
		t.Fatalf("error = %v, want ErrDesignationDatabasePathRequired", err)
	}
}

// TestE2E_CreateDesignationConcurrentSameKey — parallel retries yield one row.
func TestE2E_CreateDesignationConcurrentSameKey(t *testing.T) {
	b, _, cfg := e2eBridge(t, nil)
	dcMigrateDesignations(t, cfg.VocabDBPath)
	ctx := context.Background()

	bookID := dgSetupBook(t, b, "dc-par", "第8单元")
	ids := dgAddWords(t, b, "dcpar", 4)
	dgAssignLesson(t, b, bookID, "第8单元", ids)
	key := "req-parallel"

	var wg sync.WaitGroup
	results := make([]DesignateResult, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = b.RunCreateDesignation(ctx, dcCreateOpts(bookID, "第8单元", key, cfg.VocabDBPath))
		}(i)
	}
	wg.Wait()
	for i := 0; i < 2; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		if results[i].Status != DesignateStatusOK || results[i].DesignationID <= 0 {
			t.Fatalf("goroutine %d = %+v, want ok with id", i, results[i])
		}
	}
	if results[0].DesignationID != results[1].DesignationID {
		t.Fatalf("ids differ: %d vs %d", results[0].DesignationID, results[1].DesignationID)
	}
	if dcDesignationCount(t, cfg.VocabDBPath) != 1 {
		t.Fatalf("want exactly one designation row")
	}
	dgExpectIDs(t, dgCardIDs(results[0]), ids)
}

// TestE2E_CreateDesignationServiceStyleDBResolution mirrors cmd/service/main:
// database.path set, python.vocabDBPath empty until ResolveSharedDBPath backfill.
func TestE2E_CreateDesignationServiceStyleDBResolution(t *testing.T) {
	_, _, seedCfg := e2eBridge(t, nil)
	goPath := seedCfg.VocabDBPath
	home := t.TempDir()
	resolved, err := database.ResolveSharedDBPath(goPath, "", home)
	if err != nil {
		t.Fatalf("ResolveSharedDBPath: %v", err)
	}
	if resolved != goPath {
		t.Fatalf("resolved %q != go path %q", resolved, goPath)
	}

	cfg := seedCfg
	cfg.VocabDBPath = resolved
	lg := newFakeLogger()
	b, err := NewBridge(cfg, lg)
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	dcMigrateDesignations(t, resolved)
	ctx := context.Background()

	bookID := dgSetupBook(t, b, "dc-svc", "第4单元")
	ids := dgAddWords(t, b, "dcsvc", 2)
	dgAssignLesson(t, b, bookID, "第4单元", ids)

	res, err := b.RunCreateDesignation(ctx, dcCreateOpts(bookID, "第4单元", "req-svc-db", resolved))
	if err != nil {
		t.Fatalf("RunCreateDesignation: %v", err)
	}
	if res.Status != DesignateStatusOK {
		t.Fatalf("status = %q, want ok", res.Status)
	}
	if dcDesignationCount(t, resolved) != 1 {
		t.Fatalf("designation row missing on resolved path")
	}
	// Python subprocess must have used the same file (not temp DB_PATH default).
	if _, err := os.Stat(filepath.Join(filepath.Dir(resolved), filepath.Base(resolved))); err != nil {
		t.Fatalf("stat resolved db: %v", err)
	}
}
