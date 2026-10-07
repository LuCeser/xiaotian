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
	"math"
	"math/rand"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
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

// practiceUnavailableNotice is the fixed honest wording when the practice
// pick cannot run (nil Practice source, List failure): a coded degraded
// answer, never a silent fallback into a due-word lesson (T-13.1-14 class).
const practiceUnavailableNotice = "练习暂时取不到词，请稍后再试"

// practiceAllPracticedNotice is the honest fixed wording when the vocabulary
// has nothing left to practice today (every word was already dictated or
// practiced today): the session answers ready + this notice and ends —
// never an empty lesson.
const practiceAllPracticedNotice = "今天的词都练过啦，好好休息，明天再来吧！"

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
