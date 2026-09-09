package main

// Ticket 06 analysis plumbing: the wire shape of the analysis_result frame,
// the JSON persisted into a batch_graded session event, and the pure
// history-merging that turns past results into trends + repeat-miss words.
// The judgment logic itself lives in pkg/grading.

import (
	"encoding/json"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/grading"
)

// trendHistoryLimit bounds how many past graded sessions feed the trend and
// repeat-miss analysis — the last ten is plenty of signal for an elementary
// schooler's report and keeps the payload tiny.
const trendHistoryLimit = 10

// trendEntry is one session's outcome in the trend list (oldest first; the
// current session is appended last by applyHistory).
type trendEntry struct {
	Date    string `json:"date"` // "09-08" (month-day) from the session row
	Correct int    `json:"correct"`
	Total   int    `json:"total"`
	Score   int    `json:"score"`
}

// repeatWord is a word the student got wrong today that past sessions also
// missed — the "重点复习" signal.
type repeatWord struct {
	Word  string `json:"word"`
	Times int    `json:"times"` // past misses, not counting today
}

// analysisResultResponse is the Ticket 06 report frame sent right after
// batch_result: the session's analysis (embedded SessionReport — score,
// errors with char ops + hints, patterns, suggestions, encouragement) plus
// the cross-session view (trend list, repeat-miss words).
type analysisResultResponse struct {
	Type                  string       `json:"type"` // "analysis_result"
	SessionID             string       `json:"session_id"`
	grading.SessionReport              // embedded: total/correct_count/score/errors/patterns/suggestions/encouragement
	Trends                []trendEntry `json:"trends"`
	RepeatWords           []repeatWord `json:"repeat_words"`
}

// buildAnalysisResponse wraps a report in the wire shape with the current
// session as the only trend point (history merges in via applyHistory).
func buildAnalysisResponse(sessionID string, report grading.SessionReport) *analysisResultResponse {
	return &analysisResultResponse{
		Type:          "analysis_result",
		SessionID:     sessionID,
		SessionReport: report,
		Trends:        []trendEntry{currentTrendEntry(report, time.Now())},
		RepeatWords:   []repeatWord{},
	}
}

// currentTrendEntry renders today's outcome in the same shape as the stored
// history entries.
func currentTrendEntry(report grading.SessionReport, now time.Time) trendEntry {
	return trendEntry{
		Date:    now.Format("01-02"),
		Correct: report.CorrectCount,
		Total:   report.Total,
		Score:   report.Score,
	}
}

// storedResultWord is one word inside a stored graded result. The diff is
// deliberately NOT persisted: it is a pure function of word+heard
// (grading.GradeWord), so replay can re-derive it while the row stays small.
type storedResultWord struct {
	Word     string `json:"word"`
	Heard    string `json:"heard"`
	Correct  bool   `json:"correct"`
	NearMiss bool   `json:"near_miss"`
}

// storedGradedResult is the JSON persisted in the batch_graded session
// event: everything a parent review or trend query needs, nothing derivable.
type storedGradedResult struct {
	Category     string             `json:"category"`
	CorrectCount int                `json:"correct_count"`
	Total        int                `json:"total"`
	Results      []storedResultWord `json:"results"`
}

// marshalStoredResult renders the persistable record of a grading round.
func marshalStoredResult(category string, inputs []grading.WordInput, report grading.SessionReport) (string, error) {
	stored := storedGradedResult{
		Category:     category,
		CorrectCount: report.CorrectCount,
		Total:        report.Total,
		Results:      make([]storedResultWord, 0, len(inputs)),
	}
	for _, in := range inputs {
		stored.Results = append(stored.Results, storedResultWord{
			Word:     in.Word,
			Heard:    in.Heard,
			Correct:  in.Judgment.Correct,
			NearMiss: in.Judgment.NearMiss,
		})
	}
	b, err := json.Marshal(stored)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// applyHistory merges past graded sessions into the analysis frame: prepends
// their trend entries (oldest first, current stays last) and flags today's
// wrong words that were also missed before. Past rows are newest-first from
// ListRecentResults; unparsable payloads are skipped — history is an
// enhancement, never a gate. Rows from a DIFFERENT category are skipped too:
// an English session and a Chinese session are different word sets, and one
// trend line over both would mislead.
func applyHistory(analysis *analysisResultResponse, past []database.SessionResult, category string) {
	if analysis == nil || len(past) == 0 {
		return
	}

	missCount := make(map[string]int)
	var historyTrends []trendEntry
	for _, row := range past {
		var stored storedGradedResult
		if err := json.Unmarshal([]byte(row.Payload), &stored); err != nil {
			continue
		}
		if stored.Category != category {
			continue
		}
		for _, w := range stored.Results {
			if !w.Correct {
				missCount[w.Word]++
			}
		}
		historyTrends = append(historyTrends, trendEntry{
			Date:    shortDate(row.CreatedAt),
			Correct: stored.CorrectCount,
			Total:   stored.Total,
			Score:   grading.Percent(stored.CorrectCount, stored.Total),
		})
	}

	// past is newest-first (ListRecentResults orders by event id DESC); the
	// trend list reads oldest-first, current session last. Reversing the DB
	// order is exact — no date-string sorting to go wrong across year wraps.
	for i, j := 0, len(historyTrends)-1; i < j; i, j = i+1, j-1 {
		historyTrends[i], historyTrends[j] = historyTrends[j], historyTrends[i]
	}
	analysis.Trends = append(historyTrends, analysis.Trends...)

	// Repeat misses: today's errors that also missed before. Stable order —
	// the report already lists errors in queue order, keep that.
	for _, e := range analysis.Errors {
		if times := missCount[e.Word]; times > 0 {
			analysis.RepeatWords = append(analysis.RepeatWords, repeatWord{Word: e.Word, Times: times})
		}
	}
}

// shortDate folds a SQLite "2006-01-02 15:04:05" created_at down to "01-02",
// matching currentTrendEntry's format. Anything unparsable passes through.
func shortDate(createdAt string) string {
	if t, err := time.Parse("2006-01-02 15:04:05", createdAt); err == nil {
		return t.Format("01-02")
	}
	if len(createdAt) >= 10 && createdAt[4] == '-' {
		return createdAt[5:10]
	}
	return createdAt
}
