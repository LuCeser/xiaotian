package rt

import (
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

func TestDictationRestore(t *testing.T) {
	cards := ttCards("苹果", "香蕉")

	t.Run("mid-lesson reconnect restores cursor version and behavior", func(t *testing.T) {
		tutor := newDictationTutor(lesson.Lesson{
			LessonID: "restore", Cards: cards, CurrentIndex: 1, Version: 2,
			Outcomes: map[string]lesson.WordOutcome{
				"0": {Outcome: outcomeConfirmed, Hints: 1},
			},
		})
		if tutor.currentIndex != 1 || tutor.version != 2 || tutor.completed || tutor.checking {
			t.Fatalf("state = index %d version %d completed/checking %v/%v", tutor.currentIndex, tutor.version, tutor.completed, tutor.checking)
		}
		if tutor.perWordOutcome[0] != outcomeConfirmed || tutor.perWordHints[0] != 1 {
			t.Fatalf("restored behavior = %q/%d, want confirmed/1", tutor.perWordOutcome[0], tutor.perWordHints[0])
		}
	})

	for _, tc := range []struct {
		name         string
		stage        string
		wantChecking bool
	}{
		{name: "checking row", stage: lesson.StageChecking, wantChecking: true},
		{name: "submitted row", stage: lesson.StageSubmitted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tutor := newDictationTutor(lesson.Lesson{
				LessonID: "restore", Cards: cards, CurrentIndex: 2, Version: 3,
				Stage: tc.stage, Outcomes: map[string]lesson.WordOutcome{
					"1": {Outcome: outcomeTimeout, Hints: 2},
				},
			})
			if !tutor.completed || tutor.checking != tc.wantChecking {
				t.Fatalf("completed/checking = %v/%v, want true/%v", tutor.completed, tutor.checking, tc.wantChecking)
			}
			if tutor.perWordOutcome[1] != outcomeTimeout || tutor.perWordHints[1] != 2 {
				t.Fatalf("restored behavior = %q/%d, want timeout/2", tutor.perWordOutcome[1], tutor.perWordHints[1])
			}
		})
	}

	t.Run("legacy row restores progress without inventing behavior", func(t *testing.T) {
		tutor := newDictationTutor(lesson.Lesson{LessonID: "restore", Cards: cards, CurrentIndex: 1, Version: 2})
		if tutor.currentIndex != 1 || tutor.version != 2 || len(tutor.perWordOutcome) != 0 || len(tutor.perWordHints) != 0 {
			t.Fatalf("legacy restore = index/version %d/%d outcomes=%v hints=%v", tutor.currentIndex, tutor.version, tutor.perWordOutcome, tutor.perWordHints)
		}
	})

	t.Run("same target keeps pending hints and rejects an older snapshot", func(t *testing.T) {
		tutor := newDictationTutor(lesson.Lesson{LessonID: "restore", Cards: cards, Version: 2})
		tutor.recordHint(0)
		if !tutor.applyLessonSnapshot(lesson.Lesson{LessonID: "restore", Cards: cards, Version: 2}) {
			t.Fatal("same-version snapshot was rejected")
		}
		if tutor.perWordOutcome[0] != outcomeHint || tutor.perWordHints[0] != 1 {
			t.Fatalf("pending hint = %q/%d, want hint/1", tutor.perWordOutcome[0], tutor.perWordHints[0])
		}
		if tutor.applyLessonSnapshot(lesson.Lesson{LessonID: "restore", Cards: cards, CurrentIndex: 1, Version: 1}) {
			t.Fatal("older snapshot was accepted")
		}
		if tutor.currentIndex != 0 || tutor.version != 2 {
			t.Fatalf("older snapshot moved state to %d/%d", tutor.currentIndex, tutor.version)
		}
	})
}
