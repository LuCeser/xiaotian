// practice.go — the 13.1-05 "再练一组" pure layer (D-11/D-13): random word
// picking over the FULL vocabulary (CardRepository.List — read-only direct
// access, NEVER RunDue which takes the fetch lock and only returns due
// words), the r0x- practice lesson-id namespace that keeps practice rows out
// of every r0-YYYYMMDD daily lookup (Pitfall 10), and the next-due-days
// computation behind the no-due ending copy (D-13).
//
// FSRS isolation (D-12/T-13.1-11): the ONLY sanctioned parse of a card's
// fsrs_card JSON lives in nextDueDays' due field — a read-only, parse-failure-
// tolerant projection for the ending copy. Practice results never flow back
// into cards/review_logs (Python owns those; ADR-2026-08-01).
package rt

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// PracticeSource is the vocabulary seam the practice flow reads (13.1-05):
// the CardRepository.List method subset. The production implementation is
// database.CardRepository itself (injected in cmd/service/main.go from the
// instance the parent pages already use — zero new config); tests inject a
// fake. Read-only by construction — the interface carries no write method.
type PracticeSource interface {
	// List returns cards matching the (optionally empty) filter; an empty
	// filter lists the whole vocabulary (the single-family scale, T-13.1-12).
	List(ctx context.Context, filter database.CardFilter) ([]database.Card, error)
}

// practiceWordsLimit caps one practice set: the same ≤10 scale as the daily
// due lesson (D-11), fewer when the vocabulary minus today's words runs out.
const practiceWordsLimit = dueWordsLimit

// practiceListTimeout bounds the one vocabulary List the practice/no-due
// paths make (a read-only direct query — the same request-timeout class as
// the photo OCR pass, not the classroom pacing clock).
const practiceListTimeout = 5 * time.Second

// practiceUnavailableNotice is the fixed honest wording when the practice
// pick cannot run (nil Practice source, List failure): a coded degraded
// answer, never a silent fallback into a due-word lesson (T-13.1-14 class).
const practiceUnavailableNotice = "练习暂时取不到词，请稍后再试"

// practiceAllPracticedNotice is the honest fixed wording when the vocabulary
// has nothing left to practice today (every word was already dictated or
// practiced today): the session answers ready + this notice and ends —
// never an empty lesson.
const practiceAllPracticedNotice = "今天的词都练过啦，好好休息，明天再来吧！"

// Practice timing-event vocabulary (13.1-05; the check-stage constants live
// in check.go beside their users, timing.go untouched — same convention).
// Fields stay count-class only (T-13-06b desensitization boundary).
const (
	// EventPracticeStarted: a practice session initialized; the picked count
	// only — never the word text.
	EventPracticeStarted = "practice_started"
	// EventPracticeSubmitted: the practice submit landed its practice_logs
	// batch; fields carry the requested/recorded counts. THE audit marker
	// that a practice write happened instead of an FSRS review (T-13.1-11).
	EventPracticeSubmitted = "practice_submitted"
)

// purposePracticeSummary is the announcement purpose of the practice wrap
// line — a natural ending like purposeSummary but WITHOUT the lesson_complete
// event (the practice ending audits through EventPracticeSubmitted instead).
const purposePracticeSummary = "practice_summary"

// ReasonClientEnd names the explicit-goodbye reason (the literal
// handleEndSession has always shipped); 13.1-05 reuses it for the
// nothing-left-to-practice ending — no new reason, per plan.
const ReasonClientEnd = "client_end"

// pickPracticeWords samples up to n practice words from the vocabulary,
// EXCLUDING every word in excludeToday (the day's dictated lesson words ∪
// the day's practiced words — D-11 避开当日已默). Order is randomized with
// the supplied *rand.Rand (injectable for tests); fewer than n available
// returns all of them; nothing available returns nil (the caller answers
// the honest already-practiced notice instead of an empty lesson). Card
// ground truth (meaning/example) maps through for the hint/meaning prompts.
func pickPracticeWords(cards []database.Card, excludeToday map[string]bool, n int, rnd *rand.Rand) []lesson.Word {
	if n > practiceWordsLimit {
		n = practiceWordsLimit // the ≤10 invariant holds at the seam, D-11
	}
	avail := make([]lesson.Word, 0, len(cards))
	for _, c := range cards {
		if excludeToday[c.Word] {
			continue
		}
		avail = append(avail, lesson.Word{Word: c.Word, Meaning: c.Meaning, Example: c.Example})
	}
	if len(avail) == 0 {
		return nil
	}
	if rnd != nil {
		rnd.Shuffle(len(avail), func(i, j int) { avail[i], avail[j] = avail[j], avail[i] })
	}
	if n > 0 && len(avail) > n {
		avail = avail[:n]
	}
	return avail
}

// resolvePracticeLessonID derives the PRACTICE lesson id: "r0x-YYYYMMDD-
// HHMMSS" on the Beijing calendar (the same FixedZone discipline as
// resolveLessonID, no tzdata dependency). The r0x- prefix is the namespace
// guarantee (Pitfall 10): the daily r0-YYYYMMDD lookup can never match a
// practice row, so neither the next day's main flow nor 13.1-04's
// resume/correction shunts ever pick one up. The HHMMSS suffix keeps
// multiple same-day practice sets distinct.
func resolvePracticeLessonID(now time.Time) string {
	cst := time.FixedZone("CST", 8*3600)
	local := now.In(cst)
	return "r0x-" + local.Format("20060102") + "-" + local.Format("150405")
}

// fsrsCardDue is the single sanctioned projection of the Python-owned
// fsrs_card JSON: the due timestamp only (py-fsrs Card.to_json emits
// RFC3339 with a +00:00 offset). Everything else stays opaque.
type fsrsCardDue struct {
	Due time.Time `json:"due"`
}

// nextDueDays returns the number of whole days (rounded UP) until the
// vocabulary's nearest FUTURE due — the 「下次复习在 X 天后」 copy input of
// the no-due ending (D-13). Cards whose fsrs_card is empty/unparseable are
// skipped (legacy or new cards — never a hard failure), dues already passed
// are skipped, and a due later on TODAY's Beijing calendar day does not
// count (当天到期不算 — the day's due fetch already ran, so the honest copy
// degrades to the no-days variant when only such dues remain). Zero means
// "no presentable future due" — the caller uses the fallback copy.
func nextDueDays(cards []database.Card, now time.Time) int {
	cst := time.FixedZone("CST", 8*3600)
	today := now.In(cst).Format("2006-01-02")

	var minDue time.Time
	for _, c := range cards {
		var fc fsrsCardDue
		if err := json.Unmarshal([]byte(c.FsrsCard), &fc); err != nil {
			continue // legacy/empty/corrupt fsrs_card — skipped, never fatal
		}
		if fc.Due.IsZero() || !fc.Due.After(now) {
			continue
		}
		if fc.Due.In(cst).Format("2006-01-02") == today {
			continue // 当天到期不算
		}
		if minDue.IsZero() || fc.Due.Before(minDue) {
			minDue = fc.Due
		}
	}
	if minDue.IsZero() {
		return 0
	}
	return int(math.Ceil(minDue.Sub(now).Hours() / 24))
}

// beijingDay is the "2006-01-02" Beijing-calendar date string — the
// reviewed_day authority of practice_logs and the exclusion-set scope.
func beijingDay(now time.Time) string {
	return now.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02")
}

// noDueWordsTextForNow picks the no-due ending copy (D-13): the days variant
// when the Practice source lists the vocabulary once and a presentable
// future due exists; the day-free fallback on any degradation (nil source,
// List error, no future due). Never a fabricated count. The listing rides
// the same eligibility switches as selection (participating AND textbook
// scope, Issue #4) so "next due in N days" never counts out-of-scope words.
func (tm *TurnManager) noDueWordsTextForNow() string {
	if tm.cfg.Practice == nil {
		return noDueWordsText()
	}
	ctx, cancel := context.WithTimeout(context.Background(), practiceListTimeout)
	defer cancel()
	// Issue #10: the days-until-next-review counts THIS subject's cards only
	// (the session's category; english before session_init) — a Chinese
	// classroom's 「下次复习在 X 天后」 must not read the English column's
	// dues.
	cards, err := tm.cfg.Practice.List(ctx, database.CardFilter{
		ParticipatingOnly: true,
		ScopeEligibleOnly: true,
		Category:          tm.sessionCategory(),
	})
	if err != nil {
		tm.recorder.Emit(EventError,
			infra.F("code", ErrCodeProviderFailed),
			infra.F("at", "no_due_days_list"),
		)
		return noDueWordsText()
	}
	if days := nextDueDays(cards, time.Now()); days > 0 {
		return noDueWordsTextWithDays(days)
	}
	return noDueWordsText()
}

// handlePracticeInit is the practice=true branch of session_init (13.1-05,
// D-11): resolve an r0x- id, build today's exclusion set (the daily lesson's
// words ∪ the day's practiced words), randomly pick ≤10 words from the FULL
// vocabulary (never the due fetch), and start the SAME three-stage FSM with
// the practice flag set. Runs on the transport read loop — DB calls are the
// same short-transaction class as the main init.
func (tm *TurnManager) handlePracticeInit(s Sender) {
	if !tm.stillOpen() {
		// PR #60 review: a practice init resumed after a subject pick runs on
		// the answer turn's goroutine — a session closed under that handoff
		// answers nothing.
		return
	}
	if tm.cfg.Practice == nil || tm.cfg.PracticeStore == nil {
		// Honest degraded answer (lesson_unavailable class) — never a
		// silent fallback into a due-word lesson.
		tm.recorder.Emit(EventError,
			infra.F("code", ErrCodeLessonUnavailable),
			infra.F("practice", "no_source"),
		)
		tm.sendError(s, ErrCodeLessonUnavailable, practiceUnavailableNotice, false)
		return
	}

	now := time.Now()
	category := tm.sessionCategory()
	lessonID := resolvePracticeLessonID(now)

	// The per-lesson init lock discipline: two simultaneous practice
	// connections must not both create a row for the same id.
	unlock := lockLessonInit(lessonID)
	defer unlock()

	ctx := context.Background()

	// The exclusion set: today's dictated lesson words first (tolerate
	// ErrLessonNotFound — a day with no lesson yet means nothing to exclude
	// from that side). The daily id is THIS subject's (Issue #10): a Chinese
	// practice excludes the Chinese daily lesson's 词语, not the English
	// classroom's words.
	exclude := map[string]bool{}
	dailyID := lessonIDForCategory(tm.cfg.LessonID, now, category)
	if l, err := tm.exec.GetLessonState(ctx, dailyID); err != nil {
		if !errors.Is(err, lesson.ErrLessonNotFound) {
			tm.failLessonInit(s)
			return
		}
	} else {
		for _, c := range l.Cards {
			exclude[c.Word] = true
		}
	}

	// ...then today's already-practiced words (避开当日已默, D-11).
	practiced, err := tm.cfg.PracticeStore.ListPracticeWordsOn(ctx, beijingDay(now))
	if err != nil {
		tm.failLessonInit(s)
		return
	}
	for _, w := range practiced {
		exclude[w] = true
	}

	// Random pick over the full eligible vocabulary — participating AND
	// inside the Issue #4 textbook scope (加练 must not bypass progress;
	// read-only direct List, never RunDue's lock, T-13.1-12 accepts the
	// single-family scan scale) AND inside the session's subject (Issue #10:
	// an English practice never hands the child 语文词语, and vice versa —
	// the pick is subject-isolated at the same seam as the due fetch).
	cards, err := tm.cfg.Practice.List(ctx, database.CardFilter{
		ParticipatingOnly: true,
		ScopeEligibleOnly: true,
		Category:          category,
	})
	if err != nil {
		tm.failLessonInit(s)
		return
	}
	picked := pickPracticeWords(cards, exclude, practiceWordsLimit, rand.New(rand.NewSource(now.UnixNano()))) //nolint:gosec // G404: lesson word shuffle, not used for secrets
	if len(picked) == 0 {
		// Honest empty answer: ready + spoken notice + ack-gated ending.
		// No new reason — the client_end-style explicit end (plan).
		tm.mu.Lock()
		if tm.closed {
			// PR #60 review: same gate as every other starter.
			tm.mu.Unlock()
			return
		}
		tm.state = StateListening
		tm.sessionReadyAt = time.Now()
		tm.mu.Unlock()
		_ = s.SendControl(SessionReadyMsg{
			Type:      TypeSessionReady,
			SessionID: tm.session,
			Audio:     AudioSpec{SampleRate: requiredSampleRate, Format: requiredFormat, Channels: requiredChannels},
			TTS:       TTSSpec{SampleRate: TTSOutputSampleRate},
			Lesson:    LessonView{LessonID: lessonID, Words: []string{}},
			Category:  category,
		})
		tm.recorder.Emit(EventConnected, infra.F("session_id", tm.session))

		nctx, cancel := context.WithCancel(context.Background())
		tm.mu.Lock()
		if tm.closed {
			tm.mu.Unlock()
			cancel()
			return
		}
		tm.turnCancel = cancel
		tm.mu.Unlock()
		go tm.runNaturalEnding(s, nctx, cancel, practiceAllPracticedNotice, purposeNoDue, ReasonClientEnd)
		return
	}

	// #68 / path B: same init-write serialization as continueSessionInit —
	// the practice row either completes ahead of a racing close (the close
	// blocks on initWriteMu) or is refused outright when the close arrived
	// first. The write runs without tm.mu.
	tm.initWriteMu.Lock()
	if !tm.stillOpen() {
		tm.initWriteMu.Unlock()
		return
	}
	l, err := tm.exec.EnsureLessonCards(ctx, lessonID, picked, category)
	tm.initWriteMu.Unlock()
	if err != nil {
		tm.failLessonInit(s)
		return
	}
	tm.recorder.Emit(EventPracticeStarted, infra.F("picked", len(picked)))
	tm.startDictationSession(s, l, true)
}

// practiceSummaryText builds the practice wrap line (13.1-05, D-13's third
// ending copy): the set's battle count in practice wording — distinct from
// checkedSummaryText (lesson_complete) and noDueWordsText (no_due_words),
// never a shared template. A failed practice-record write appends the honest
// save-failure suffix (S-05: never a fabricated success).
func practiceSummaryText(total, correct int, saveFailed bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "这组练习 %d 个词对了 %d 个，很棒！", total, correct)
	if saveFailed {
		b.WriteString(reviewFailedSuffix())
	}
	return b.String()
}

// submitPractice is the practice check_submit (13.1-05): the SAME per-word
// manual gate (a practice check is still a check — D-05), the SAME
// CompleteCheck stage latch, but past the gate the per-word D-02 ratings go
// to PracticeStore.RecordPractice INSTEAD of WordSource.SubmitReview
// (T-13.1-11: the FSRS write channel is never touched). The honest
// review_result reports the record counts, the practice wrap line speaks
// through the ACK gate, and the ending carries practice_complete.
func (tm *TurnManager) submitPractice(s Sender, m CheckSubmitMsg) {
	if m.RequestID == "" {
		tm.sendError(s, ErrCodeInvalidMessage, checkVerdictInvalidNotice, true)
		return
	}

	// Submit gate (D-05, identical to every other submit): every word needs
	// a MANUAL verdict. The word list is captured under the lock here — the
	// finishing goroutine reads the copy, never the live tutor.
	tm.mu.Lock()
	lessonID := tm.tutor.lessonID
	expected := tm.tutor.version
	total := len(tm.tutor.words)
	wordsList := make([]lesson.Word, total)
	copy(wordsList, tm.tutor.words)
	manual := make(map[int]bool, total)
	for _, v := range tm.tutor.checkVerdicts {
		if v.Manual {
			manual[v.WordIndex] = true
		}
	}
	tm.mu.Unlock()
	if len(manual) < total {
		tm.sendError(s, ErrCodeInvalidMessage, checkSubmitIncompleteNotice, true)
		return
	}

	// Latch: the same stage transition — only its first commit proceeds
	// (the replay/idempotency protection is the store's, not a session
	// memory artifact).
	ctx, cancel := context.WithCancel(context.Background())
	tm.mu.Lock()
	tm.turnCancel = cancel
	tm.mu.Unlock()

	res, err := tm.exec.CompleteCheck(ctx, lessonID, expected, m.RequestID, false)
	if err != nil {
		var conflict *lesson.ConflictError
		if errors.As(err, &conflict) {
			tm.mu.Lock()
			tm.tutor.checking = false
			tm.mu.Unlock()
			tm.recorder.Emit(EventError,
				infra.F("code", ErrCodeDuplicateSubmit),
				infra.F("duplicate", "practice_submit_conflict"),
			)
			return
		}
		tm.sendError(s, ErrCodeProviderFailed, checkStoreFailedNotice, true)
		return
	}
	if res.Idempotent {
		// The latch already fell elsewhere: nothing to write, nothing to
		// replay (the honest no-op).
		tm.mu.Lock()
		tm.tutor.checking = false
		tm.mu.Unlock()
		tm.recorder.Emit(EventError,
			infra.F("code", ErrCodeDuplicateSubmit),
			infra.F("duplicate", "practice_submit_latch"),
		)
		return
	}

	tm.mu.Lock()
	tm.tutor.version = res.NewVersion
	tm.tutor.checking = false
	tm.mu.Unlock()

	go tm.finishPracticeSubmit(s, ctx, cancel, lessonID, wordsList, res, m.RequestID)
}

// finishPracticeSubmit runs on its own goroutine (the ACK-gated ending
// blocks; the read loop must keep serving acks): ONE RecordPractice batch
// from the STORED verdicts + outcomes readback (the row is the authority),
// the honest practice feedback, the wrap line, then
// session_ended(practice_complete). 13.3-01 brackets the write with the
// two-phase save receipt — whose saved/failed expresses the PRACTICE
// STORAGE result (practice_logs), NOT an FSRS write (practice never enters
// the FSRS schedule, D-11).
func (tm *TurnManager) finishPracticeSubmit(s Sender, ctx context.Context, cancel context.CancelFunc, lessonID string, wordsList []lesson.Word, res lesson.CompleteCheckResult, requestID string) {
	defer cancel()

	// The D-02 matrix over the STORED readback — the same single rating
	// discipline as the main submit (never a second table, Pitfall 1).
	ratings := checkRatings(wordsList, res.Verdicts, res.Outcomes)

	correct := 0
	correctByWord := make(map[string]bool, len(res.Verdicts))
	for _, v := range res.Verdicts {
		if v.WordIndex < 0 || v.WordIndex >= len(wordsList) {
			continue
		}
		correctByWord[wordsList[v.WordIndex].Word] = v.Correct
		if v.Correct {
			correct++
		}
	}
	hintsByWord := make(map[string]int, len(res.Outcomes))
	for key, o := range res.Outcomes {
		idx, perr := strconv.Atoi(key)
		if perr != nil || idx < 0 || idx >= len(wordsList) {
			continue
		}
		hintsByWord[wordsList[idx].Word] = o.Hints
	}

	day := beijingDay(time.Now())
	records := make([]lesson.PracticeRecord, 0, len(ratings))
	for word, rating := range ratings {
		records = append(records, lesson.PracticeRecord{
			LessonID:    lessonID,
			Word:        word,
			Rating:      rating,
			Correct:     correctByWord[word],
			Hints:       hintsByWord[word],
			ReviewedDay: day,
		})
	}

	total := len(wordsList)
	requested := len(records)
	recorded := 0
	saveFailed := false
	if requested > 0 {
		// 13.3-01 receipt phase one: pending marker before the practice
		// write (same bypass discipline as every mount — a receipt failure
		// never returns, never rewrites, S-05).
		receiptID := int64(0)
		requestedSet := sortedWordSet(ratings)
		if tm.cfg.Receipts != nil {
			rec, rerr := tm.cfg.Receipts.Begin(ctx, lesson.ReceiptInput{
				LessonID:  lessonID,
				RequestID: requestID,
				Kind:      lesson.ReceiptKindPractice,
				Requested: requestedSet,
			})
			if rerr != nil || rec.ID <= 0 {
				tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed), infra.F("at", "save_receipt"))
			} else {
				receiptID = rec.ID
			}
		}

		if err := tm.cfg.PracticeStore.RecordPractice(ctx, records); err != nil {
			// Honest failure: the practice records were NOT saved — never a
			// narrated success (transparency prohibition, S-05).
			saveFailed = true
			tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed))
			// 13.3-01 receipt phase two (failed): the PRACTICE STORAGE failed
			// — this receipt says nothing about FSRS (zero SubmitReview calls
			// on this path by construction, T-13.1-11). missing = the full
			// attempted set, updated empty.
			if tm.cfg.Receipts != nil && receiptID > 0 {
				if cerr := tm.cfg.Receipts.Complete(ctx, receiptID, nil, requestedSet, false); cerr != nil {
					tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed), infra.F("at", "save_receipt"))
				}
			}
		} else {
			recorded = requested
			tm.recorder.Emit(EventPracticeSubmitted,
				infra.F("requested", requested),
				infra.F("recorded", recorded),
			)
			// 13.3-01 receipt phase two (saved): every record landed —
			// updated = the requested set, missing empty. The receipt's saved
			// means practice_logs storage succeeded, NOT an FSRS write (D-11).
			if tm.cfg.Receipts != nil && receiptID > 0 {
				if cerr := tm.cfg.Receipts.Complete(ctx, receiptID, requestedSet, nil, true); cerr != nil {
					tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed), infra.F("at", "save_receipt"))
				}
			}
		}
	}

	tm.mu.Lock()
	tm.tutor.reviewSeen = true
	tm.mu.Unlock()

	_ = s.SendControl(ReviewResultMsg{
		Type:      TypeReviewResult,
		Requested: requested,
		Updated:   recorded,
		Missing:   0,
		Unchecked: false,
	})

	summary := practiceSummaryText(total, correct, saveFailed)
	tm.runNaturalEnding(s, ctx, cancel, summary, purposePracticeSummary, ReasonPracticeComplete)
}
