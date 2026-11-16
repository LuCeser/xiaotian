package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
	"github.com/xiaotian/home-tutor-agent/pkg/ocr"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
	"github.com/xiaotian/home-tutor-agent/pkg/rt"
)

type exampleOutcome struct {
	Saved   int              `json:"saved"`
	Skipped int              `json:"skipped"`
	Missing []exampleMissing `json:"missing"`
}
type exampleMissing struct {
	ID     int64  `json:"id"`
	Reason string `json:"reason"`
}

// A request owns one bounded model call. No background task can outlive it.
func (h *handler) fillPhraseExamples(ctx context.Context, ids []int64, bookID int64) exampleOutcome {
	out := exampleOutcome{Missing: []exampleMissing{}}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	inputs := []ocr.PhraseExampleInput{}
	candidates := map[int64]database.Card{}
	seen := map[int64]bool{}
	resolver := rt.ExampleResolver{Cards: h.cards, Textbooks: h.textbooks}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		card, err := h.cards.GetByID(ctx, id)
		if err != nil {
			out.Missing = append(out.Missing, exampleMissing{id, "target_unavailable"})
			continue
		}
		if card.Category != CategoryEnglish || !ocr.IsEnglishPhraseCandidate(card.Word) || strings.TrimSpace(card.Example) != "" {
			out.Skipped++
			continue
		}
		reason := ""
		switch {
		case strings.TrimSpace(card.Meaning) == "":
			reason = "no_meaning"
		case len(inputs) >= 10:
			reason = "batch_limit"
		case h.generator == nil:
			reason = "unavailable"
		}
		if reason != "" {
			out.Missing = append(out.Missing, exampleMissing{id, reason})
			continue
		}
		ec, err := resolver.ExampleContext(ctx, lesson.Word{Word: card.Word})
		if err != nil {
			out.Missing = append(out.Missing, exampleMissing{id, "context_unavailable"})
			continue
		}
		// Import creates/chooses a confirmed book before assigning the cards.
		if bookID > 0 {
			if h.textbooks == nil {
				out.Missing = append(out.Missing, exampleMissing{id, "context_unavailable"})
				continue
			}
			book, err := h.textbooks.Get(ctx, bookID)
			if err != nil || book.Category != CategoryEnglish {
				out.Missing = append(out.Missing, exampleMissing{id, "context_unavailable"})
				continue
			}
			ec.HasTextbook = true
			ec.Edition, ec.Stage, ec.Grade, ec.Volume, ec.Module = book.Edition, book.Stage, book.Grade, book.Volume, book.Module
			ec.LearnedLessons = book.LearnedLessons
		}
		background := ocr.PhraseExampleContext{
			Textbook:     ocr.MeaningTextbook{Edition: ec.Edition, Stage: ec.Stage, Grade: ec.Grade, Volume: ec.Volume, Module: ec.Module},
			LearnedWords: ec.LearnedWords, Lesson: ec.LessonName,
		}
		if ec.HasTextbook {
			background.LearnedLessons = &ec.LearnedLessons
		}
		inputs = append(inputs, ocr.PhraseExampleInput{ID: id, Word: card.Word, Meaning: card.Meaning, Context: background})
		candidates[id] = card
	}
	if len(inputs) == 0 {
		return out
	}
	started := time.Now()
	genCtx, genCancel := context.WithTimeout(ctx, 8*time.Second)
	generated, err := h.generator.GeneratePhraseExamples(genCtx, inputs)
	genCancel()
	for _, input := range inputs {
		reason := "invalid_output"
		if err != nil {
			reason = err.Error()
		} else if sentence, ok := generated[input.ID]; ok {
			card := candidates[input.ID]
			result, saveErr := h.exec.RunEdit(ctx, pybridge.EditOpts{ID: input.ID, Example: &sentence, ExpectedExampleRevision: &card.ExampleRevision})
			if saveErr != nil {
				reason = "save_failed"
			} else if !result.Applied {
				reason = "target_changed"
			} else {
				out.Saved++
				continue
			}
		}
		out.Missing = append(out.Missing, exampleMissing{input.ID, reason})
	}
	reasons := []string{}
	for _, missing := range out.Missing {
		reasons = append(reasons, missing.Reason)
	}
	h.logger.Info("phrase examples completed", infra.F("source", "generated"), infra.F("duration_ms", time.Since(started).Milliseconds()), infra.F("failure_reasons", reasons))
	return out
}

func (h *handler) generatePhraseExamples(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.IDs) < 1 || len(req.IDs) > 10 {
		writeError(w, 400, codeInvalidRequest, "ids must contain 1..10 entries")
		return
	}
	for _, id := range req.IDs {
		if id <= 0 {
			writeError(w, 400, codeInvalidRequest, "invalid word id")
			return
		}
	}
	writeJSON(w, h.fillPhraseExamples(r.Context(), req.IDs, 0))
}
