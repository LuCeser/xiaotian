package rt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// ---------------------------------------------------------------------------
// Issue #9 (ADR-0006): on-demand example sentences — prompt contract,
// lenient parse, context resolver, and the dispatch-stage augmentation.
// ---------------------------------------------------------------------------

// generationScript builds one generation ChatResponse carrying the strict
// {"reply":...} JSON as its content (intentScript's twin for the second
// call).
func generationScript(reply string) llmScript {
	payload, _ := json.Marshal(map[string]string{"reply": reply})
	wire := `{"choices":[{"index":0,"message":{"role":"assistant","content":` + strconv.Quote(string(payload)) + `}}]}`
	return llmScript{wire: wire}
}

// ttCardsNoExample builds cards with a confirmed meaning but NO stored
// example — the only shape that grounds on-demand generation.
func ttCardsNoExample(words ...string) []lesson.Word {
	cards := make([]lesson.Word, 0, len(words))
	for _, w := range words {
		cards = append(cards, lesson.Word{Word: w, Meaning: "释义-" + w})
	}
	return cards
}

// ttLastEventField returns the given field of the LAST rt_event entry of
// the named event ("" when absent) — the example_served result label.
func ttLastEventField(t *testing.T, log *captureLogger, event, key string) string {
	t.Helper()
	value := ""
	for _, e := range log.snapshot() {
		if e.msg != "rt_event" {
			continue
		}
		match, found := false, false
		for _, f := range e.fields {
			if f.Key == "event" && f.Value == event {
				match = true
			}
		}
		if !match {
			continue
		}
		for _, f := range e.fields {
			if f.Key == key {
				value = fmt.Sprintf("%v", f.Value)
				found = true
			}
		}
		if !found {
			t.Fatalf("event %s carried no field %s", event, key)
		}
	}
	if value == "" {
		t.Fatalf("no rt_event %s recorded", event)
	}
	return value
}

// fakeExampleSource is the canned ExampleSource seam for turn tests.
type fakeExampleSource struct {
	calls []lesson.Word
	err   error
	ec    ExampleContext
	mu    sync.Mutex
}

func (f *fakeExampleSource) ExampleContext(_ context.Context, w lesson.Word) (ExampleContext, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, w)
	return f.ec, f.err
}

func (f *fakeExampleSource) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// ---------------------------------------------------------------------------
// Prompt contract (buildExampleMessages)
// ---------------------------------------------------------------------------

// TestBuildExampleMessagesCarriesConfirmedContext: the generation prompt
// pins the confirmed meaning, the textbook metadata with unregistered
// fields omitted, the word's lesson, the learned-lessons scale and the
// learned-word sample — and NEVER a region (US-19; ADR-0006 #3).
func TestBuildExampleMessagesCarriesConfirmedContext(t *testing.T) {
	ec := ExampleContext{
		HasTextbook:    true,
		Edition:        "人教版PEP",
		Stage:          "小学",
		Grade:          "三年级",
		Volume:         "上册",
		Module:         "", // unregistered: omitted, not blanked
		LessonName:     "Unit 3",
		LearnedLessons: 4,
		LearnedWords:   []string{"cat", "dog", "red"},
	}
	msgs := buildExampleMessages(exampleKindMeaning, "apple", "n. 苹果", ec)
	if len(msgs) != 2 || msgs[0].Role != "system" || msgs[1].Role != "user" {
		t.Fatalf("messages = %+v, want [system user]", msgs)
	}
	system, user := msgs[0].Content, msgs[1].Content
	for _, want := range []string{
		"严禁任何羞辱", // persona carried verbatim
		"先用一两句简单中文讲清这个词的意思",
		"例句必须贴合已确认释义",
		"优先只使用「已学词汇」列表里的词",
		`{"reply":`,
	} {
		if !contains(system, want) {
			t.Errorf("example system prompt missing %q", want)
		}
	}
	for _, want := range []string{
		"词：apple",
		"已确认释义：n. 苹果",
		"教材背景：版本 人教版PEP · 学段 小学 · 年级 三年级 · 册 上册",
		"词的课次：Unit 3",
		"学习进度：这本教材已学完第 4 课（含第 4 课）",
		"已学词汇（例句优先只用这些词和最基础的常见词）：cat, dog, red",
	} {
		if !contains(user, want) {
			t.Errorf("example user prompt missing %q", want)
		}
	}
	if contains(user, "模块") {
		t.Error("unregistered module field rendered — want omitted")
	}
	if contains(user, "地区") || contains(system, "地区") {
		t.Error("region leaked into the example prompt — US-19 forbids it")
	}
}

// TestBuildExampleMessagesHonestAbsences: no textbook renders the honest
// supplementary line, a zero learned scale renders 暂无已学课次, and an
// empty sample degrades to the basic-words line — never a fabricated
// background.
func TestBuildExampleMessagesHonestAbsences(t *testing.T) {
	msgs := buildExampleMessages(exampleKindExample, "apple", "n. 苹果", ExampleContext{})
	system, user := msgs[0].Content, msgs[1].Content
	for _, want := range []string{
		"教材背景：无教材归属，这是一个课外补充词汇",
		"学习进度：暂无教材课次记录",
		"已学词汇（例句优先只用这些词和最基础的常见词）：暂无记录，只用最基础的常见词",
	} {
		if !contains(user, want) {
			t.Errorf("supplementary user prompt missing %q", want)
		}
	}
	if !contains(system, "直接给一个用到该词的简单英语例句") {
		t.Error("example-kind system prompt missing the sentence-first task line")
	}

	registered := buildExampleMessages(exampleKindMeaning, "apple", "n. 苹果", ExampleContext{
		HasTextbook: true, Edition: "人教版PEP",
	})[1].Content
	if !contains(registered, "学习进度：这本教材暂无已学课次") {
		t.Errorf("zero-learned scale = %q, want the honest 暂无已学课次 line", registered)
	}
}

// ---------------------------------------------------------------------------
// Lenient parse (parseExampleReply)
// ---------------------------------------------------------------------------

func TestParseExampleReply(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"strict json", `{"reply":"比如：I eat an apple."}`, "比如：I eat an apple.", true},
		{"sentence key", `{"sentence":"Apple is red."}`, "Apple is red.", true},
		{"fenced", "```json\n{\"reply\":\"比如：Apple.\"}\n```", "比如：Apple.", true},
		{"prose wrapped", `好的老师：{"reply":"比如：Apple is sweet."} 完毕`, "比如：Apple is sweet.", true},
		{"empty", `{"reply":"  "}`, "", false},
		{"not json", "I eat an apple.", "", false},
		{"non string", `{"reply":123}`, "", false},
		{"missing key", `{"text":"x"}`, "", false},
	}
	for _, c := range cases {
		got, ok := parseExampleReply(c.in)
		if ok != c.ok {
			t.Errorf("%s: ok = %v, want %v", c.name, ok, c.ok)
			continue
		}
		if ok && got != c.want {
			t.Errorf("%s: reply = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestParseExampleReplyOverlongRejected: a bloated reply is rejected whole,
// never truncated mid-sentence for a child to hear.
func TestParseExampleReplyOverlongRejected(t *testing.T) {
	long := strings.Repeat("好", maxExampleReplyRunes+1)
	if _, ok := parseExampleReply(`{"reply":"` + long + `"}`); ok {
		t.Error("over-long reply accepted, want rejected")
	}
	atCap := strings.Repeat("好", maxExampleReplyRunes)
	if got, ok := parseExampleReply(`{"reply":"` + atCap + `"}`); !ok || got != atCap {
		t.Errorf("at-cap reply = (%d runes, %v), want accepted verbatim", len([]rune(got)), ok)
	}
}

// ---------------------------------------------------------------------------
// Context resolver (ExampleResolver)
// ---------------------------------------------------------------------------

// fakeExampleCards is the narrow card-lister seam.
type fakeExampleCards struct {
	err  error
	list []database.Card
}

func (f *fakeExampleCards) List(_ context.Context, filter database.CardFilter) ([]database.Card, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]database.Card, 0, len(f.list))
	for _, c := range f.list {
		if filter.Category != "" && c.Category != filter.Category {
			continue
		}
		if filter.ParticipatingOnly && !c.Participates {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

// fakeExampleBooks is the narrow textbook seam.
type fakeExampleBooks struct {
	books       map[int64]database.Textbook
	assignments map[int64][]database.TextbookAssignment
	getErr      map[int64]error
}

func (f *fakeExampleBooks) Get(_ context.Context, id int64) (database.Textbook, error) {
	if err, ok := f.getErr[id]; ok {
		return database.Textbook{}, err
	}
	b, ok := f.books[id]
	if !ok {
		return database.Textbook{}, database.ErrTextbookNotFound
	}
	return b, nil
}

func (f *fakeExampleBooks) AssignmentsByCardIDs(_ context.Context, ids []int64) (map[int64][]database.TextbookAssignment, error) {
	out := make(map[int64][]database.TextbookAssignment, len(ids))
	for _, id := range ids {
		out[id] = f.assignments[id]
	}
	return out, nil
}

func exampleCard(id int64, word string, participates bool) database.Card {
	return database.Card{ID: id, Word: word, Category: exampleCategoryEnglish, Participates: participates}
}

// TestExampleResolverUngrounded: a word with no English card (Chinese
// content, deleted card) refuses to ground — ErrExampleUngrounded, never a
// fabricated context. An unwired resolver reports the assembly bug as
// ErrExampleUnwired — the word is never blamed for a wiring gap.
func TestExampleResolverUngrounded(t *testing.T) {
	r := &ExampleResolver{
		Cards: &fakeExampleCards{list: []database.Card{
			{ID: 1, Word: "古诗", Category: "chinese"},
		}},
		Textbooks: &fakeExampleBooks{},
	}
	if _, err := r.ExampleContext(context.Background(), lesson.Word{Word: "古诗", Meaning: "课文"}); !errors.Is(err, ErrExampleUngrounded) {
		t.Errorf("err = %v, want ErrExampleUngrounded", err)
	}
	if _, err := (&ExampleResolver{}).ExampleContext(context.Background(), lesson.Word{Word: "apple"}); !errors.Is(err, ErrExampleUnwired) {
		t.Errorf("unwired err = %v, want ErrExampleUnwired", err)
	}
}

// TestExampleResolverSupplementaryAndSample: an English card with no
// attribution resolves the honest supplementary context; the sample lists
// eligible words minus the current word and honors the cap.
func TestExampleResolverSupplementaryAndSample(t *testing.T) {
	list := []database.Card{
		exampleCard(1, "apple", true),
		exampleCard(2, "cat", true),
		exampleCard(3, "dog", false), // opted out: not sample material
		exampleCard(4, "egg", true),
	}
	r := &ExampleResolver{Cards: &fakeExampleCards{list: list}, Textbooks: &fakeExampleBooks{}}
	ec, err := r.ExampleContext(context.Background(), lesson.Word{Word: "apple", Meaning: "n. 苹果"})
	if err != nil {
		t.Fatalf("ExampleContext: %v", err)
	}
	if ec.HasTextbook {
		t.Error("HasTextbook = true for an unattributed word, want the supplementary context")
	}
	if len(ec.LearnedWords) != 2 || ec.LearnedWords[0] != "cat" || ec.LearnedWords[1] != "egg" {
		t.Errorf("learned sample = %v, want [cat egg] (no current word, no opted-out)", ec.LearnedWords)
	}
}

// TestExampleResolverPicksLearnedSource: when the word rides several
// textbooks, the pick prefers the source whose lesson is already learned
// (that is why the word reached the classroom), then the primary book, then
// stable id order (ADR-0006 #3).
func TestExampleResolverPicksLearnedSource(t *testing.T) {
	primary := database.Textbook{ID: 7, Name: "主教材", Edition: "人教版PEP", IsPrimary: true,
		Lessons: []string{"Unit 1", "Unit 2", "Unit 3"}, LearnedLessons: 1} // Unit 1 learned
	other := database.Textbook{ID: 9, Name: "课外班教材", Edition: "外研社",
		Lessons: []string{"Lesson A", "Lesson B"}, LearnedLessons: 2} // both learned
	r := &ExampleResolver{
		Cards: &fakeExampleCards{list: []database.Card{exampleCard(1, "apple", true)}},
		Textbooks: &fakeExampleBooks{
			books: map[int64]database.Textbook{7: primary, 9: other},
			assignments: map[int64][]database.TextbookAssignment{
				1: {
					{TextbookID: 7, Lesson: "Unit 3"},   // primary but NOT yet learned
					{TextbookID: 9, Lesson: "Lesson B"}, // learned source wins
				},
			},
		},
	}
	ec, err := r.ExampleContext(context.Background(), lesson.Word{Word: "apple", Meaning: "n. 苹果"})
	if err != nil {
		t.Fatalf("ExampleContext: %v", err)
	}
	if !ec.HasTextbook || ec.Edition != "外研社" || ec.LessonName != "Lesson B" || ec.LearnedLessons != 2 {
		t.Errorf("pick = %+v, want the learned source (外研社 Lesson B, scale 2)", ec)
	}
}

// TestExampleResolverSampleCap: the learned-word sample never exceeds
// maxLearnedSampleWords.
func TestExampleResolverSampleCap(t *testing.T) {
	list := make([]database.Card, 0, maxLearnedSampleWords+20)
	list = append(list, exampleCard(1, "apple", true))
	for i := 0; i < maxLearnedSampleWords+20; i++ {
		list = append(list, exampleCard(int64(2+i), fmt.Sprintf("w%d", i), true))
	}
	r := &ExampleResolver{Cards: &fakeExampleCards{list: list}, Textbooks: &fakeExampleBooks{}}
	ec, err := r.ExampleContext(context.Background(), lesson.Word{Word: "apple", Meaning: "n. 苹果"})
	if err != nil {
		t.Fatalf("ExampleContext: %v", err)
	}
	if len(ec.LearnedWords) != maxLearnedSampleWords {
		t.Errorf("sample size = %d, want the %d cap", len(ec.LearnedWords), maxLearnedSampleWords)
	}
	for _, w := range ec.LearnedWords {
		if w == "apple" {
			t.Error("sample contains the current word")
		}
	}
}

// ttTTSJoined returns every text the fake TTS synthesized, joined — the
// SPOKEN channel (student mode masks the screen copy, never the voice).
func ttTTSJoined(tm *TurnManager) string {
	return strings.Join(tm.tts.(*fakeTTS).recordedTexts(), "")
}

// ---------------------------------------------------------------------------
// Dispatch-stage augmentation (exampleReply + dispatchTurn guard)
// ---------------------------------------------------------------------------

// ttExampleFixture is ttIntentFixture with the Examples seam injected.
func ttExampleFixture(t *testing.T, cards []lesson.Word, asr *fakeASR, llm *fakeLLM, src ExampleSource) (*TurnManager, *captureSender, *captureLogger, *fakeExecutor) {
	t.Helper()
	cfg := ttConfigWith(newFakeWords(cards), func(c *TurnManagerConfig) {
		c.Examples = src
	})
	return ttIntentFixtureCfg(t, cards, asr, llm, cfg)
}

// TestExampleIntentGeneratesOnDemand: a 求例句 turn with a confirmed
// meaning and no stored example makes exactly TWO LLM calls (classify +
// generate), speaks the GENERATED reply (not the classification fallback),
// emits example_served/generated — and the practice position never moves.
func TestExampleIntentGeneratesOnDemand(t *testing.T) {
	cards := ttCardsNoExample("apple", "banana")
	asr := newFakeASR(asrScript{text: "这个单词怎么用"})
	src := &fakeExampleSource{ec: ExampleContext{HasTextbook: true, Edition: "人教版PEP", LearnedLessons: 3, LearnedWords: []string{"cat"}}}
	llm := newFakeLLM(
		intentScript(intentExample, "老师这边还没有这个例句，你先凭印象写吧"),
		generationScript("这个词是苹果的意思。比如：I eat an apple."),
	)
	tm, s, log, exec := ttExampleFixture(t, cards, asr, llm, src)
	prev := s.countOf(TypeTeacherSentence)

	ttRunIntentTurn(t, tm, s, prev)

	if llm.callCount() != 2 {
		t.Fatalf("llm calls = %d, want 2 (classify + generate)", llm.callCount())
	}
	// Student mode (D-10): the SCREEN copy masks the dictation word; the
	// voice speaks the generated sentence in full.
	if got := ttSentencesSince(t, s, prev); got != "这个词是苹果的意思。比如：I eat an•••." {
		t.Errorf("display reply = %q, want the masked generated sentence", got)
	}
	if !contains(ttTTSJoined(tm), "I eat an apple.") {
		t.Errorf("spoken text = %q, want the full generated sentence", ttTTSJoined(tm))
	}
	if contains(ttSentencesSince(t, s, prev), "apple") {
		t.Error("screen copy leaked the dictation word")
	}
	if got := ttEventCount(t, log, EventExampleServed); got != 1 {
		t.Fatalf("example_served events = %d, want 1", got)
	}
	if got := ttLastEventField(t, log, EventExampleServed, "result"); got != exampleResultGenerated {
		t.Errorf("example_served result = %q, want generated", got)
	}
	if src.callCount() != 1 {
		t.Errorf("context resolutions = %d, want 1", src.callCount())
	}
	// The generation request carries the confirmed context.
	gen := llm.calls[1].messages
	if !contains(gen[1].Content, "已确认释义：释义-apple") || !contains(gen[1].Content, "人教版PEP") {
		t.Errorf("generation user prompt = %q, want confirmed meaning + textbook context", gen[1].Content)
	}
	// Position discipline: no advance, no lesson_update, still on word 1.
	if exec.advanceCount() != 0 {
		t.Errorf("advance calls = %d, want 0 (help never moves the cursor)", exec.advanceCount())
	}
	if n := s.countOf(TypeLessonUpdate); n != 0 {
		t.Errorf("lesson_update count = %d, want 0", n)
	}
	tm.mu.Lock()
	idx := tm.tutor.currentIndex
	tm.mu.Unlock()
	if idx != 0 {
		t.Errorf("currentIndex = %d, want 0", idx)
	}
}

// TestMeaningIntentAugmentsWithExample: 问词义 gets the same on-demand
// sentence — the meaning-kind task line rides the generation prompt.
func TestMeaningIntentAugmentsWithExample(t *testing.T) {
	cards := ttCardsNoExample("apple", "banana")
	asr := newFakeASR(asrScript{text: "苹果什么意思"})
	src := &fakeExampleSource{ec: ExampleContext{}}
	llm := newFakeLLM(
		intentScript(intentMeaning, "这个词是苹果的意思。"),
		generationScript("苹果是一种水果。比如：The apple is sweet."),
	)
	tm, s, log, _ := ttExampleFixture(t, cards, asr, llm, src)
	prev := s.countOf(TypeTeacherSentence)

	ttRunIntentTurn(t, tm, s, prev)

	if llm.callCount() != 2 {
		t.Fatalf("llm calls = %d, want 2", llm.callCount())
	}
	if got := ttSentencesSince(t, s, prev); got != "苹果是一种水果。比如：The••• is sweet." {
		t.Errorf("display reply = %q, want the masked augmented reply", got)
	}
	if !contains(ttTTSJoined(tm), "The apple is sweet.") {
		t.Errorf("spoken text = %q, want the full augmented reply", ttTTSJoined(tm))
	}
	if got := ttLastEventField(t, log, EventExampleServed, "result"); got != exampleResultGenerated {
		t.Errorf("result = %q, want generated", got)
	}
	if sys := llm.calls[1].messages[0].Content; !contains(sys, "先用一两句简单中文讲清这个词的意思") {
		t.Error("meaning-kind generation prompt missing the meaning-first task line")
	}
}

// TestExampleGenerationFailsHonestly: a failed generation (call error OR
// unusable output) speaks the classification fallback reply and emits
// example_served/fallback — never a fabricated sentence, and the classroom
// keeps going.
func TestExampleGenerationFailsHonestly(t *testing.T) {
	for _, tc := range []struct {
		name string
		gen  llmScript
	}{
		{gen: llmScript{err: fmt.Errorf("provider down")}, name: "call error"},
		{gen: llmScript{wire: `{"choices":[{"message":{"content":"我觉得你想说苹果"}}]}`}, name: "unparseable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cards := ttCardsNoExample("apple", "banana")
			asr := newFakeASR(asrScript{text: "造个句"})
			src := &fakeExampleSource{ec: ExampleContext{}}
			fallback := "老师这边还没有这个例句，你先凭印象写吧"
			llm := newFakeLLM(intentScript(intentExample, fallback), tc.gen)
			tm, s, log, _ := ttExampleFixture(t, cards, asr, llm, src)
			prev := s.countOf(TypeTeacherSentence)

			ttRunIntentTurn(t, tm, s, prev)

			if got := ttSentencesSince(t, s, prev); got != fallback {
				t.Errorf("spoken reply = %q, want the honest fallback", got)
			}
			if got := ttLastEventField(t, log, EventExampleServed, "result"); got != exampleResultFallback {
				t.Errorf("result = %q, want fallback", got)
			}
		})
	}
}

// TestExampleServedFromStoredExample: a stored textbook example is quoted
// by the classification reply alone — NO second call (不替换教材原文),
// result stored.
func TestExampleServedFromStoredExample(t *testing.T) {
	cards := ttCards("apple", "banana") // Example filled
	asr := newFakeASR(asrScript{text: "怎么用"})
	src := &fakeExampleSource{}
	llm := newFakeLLM(intentScript(intentExample, "例句-apple"))
	tm, s, log, _ := ttExampleFixture(t, cards, asr, llm, src)
	prev := s.countOf(TypeTeacherSentence)

	ttRunIntentTurn(t, tm, s, prev)

	if llm.callCount() != 1 {
		t.Fatalf("llm calls = %d, want 1 (stored example, no generation)", llm.callCount())
	}
	if src.callCount() != 0 {
		t.Errorf("context resolutions = %d, want 0", src.callCount())
	}
	if got := ttSentencesSince(t, s, prev); got != "•••" {
		t.Errorf("display reply = %q, want the stored example masked whole (normalization swallows the 例句- prefix)", got)
	}
	if !contains(ttTTSJoined(tm), "例句-apple") {
		t.Errorf("spoken text = %q, want the stored example verbatim", ttTTSJoined(tm))
	}
	if got := ttLastEventField(t, log, EventExampleServed, "result"); got != exampleResultStored {
		t.Errorf("result = %q, want stored", got)
	}
}

// TestExampleNoMeaningNoGeneration: without a confirmed meaning nothing
// grounds a sentence — the fixed honest wording speaks, one call only,
// result no_meaning.
func TestExampleNoMeaningNoGeneration(t *testing.T) {
	cards := []lesson.Word{{Word: "apple"}, {Word: "banana"}}
	asr := newFakeASR(asrScript{text: "什么意思"})
	src := &fakeExampleSource{}
	fallback := "老师这边没有这个词的释义，你凭印象写吧"
	llm := newFakeLLM(intentScript(intentMeaning, fallback))
	tm, s, log, _ := ttExampleFixture(t, cards, asr, llm, src)
	prev := s.countOf(TypeTeacherSentence)

	ttRunIntentTurn(t, tm, s, prev)

	if llm.callCount() != 1 {
		t.Fatalf("llm calls = %d, want 1 (no meaning, no generation)", llm.callCount())
	}
	if got := ttSentencesSince(t, s, prev); got != fallback {
		t.Errorf("spoken reply = %q, want the fixed honest wording", got)
	}
	if got := ttLastEventField(t, log, EventExampleServed, "result"); got != exampleResultNoMeaning {
		t.Errorf("result = %q, want no_meaning", got)
	}
}

// TestExampleNilSeamDegrades: without the Examples seam (old wiring,
// unit-built managers) the fallback reply still speaks honestly.
func TestExampleNilSeamDegrades(t *testing.T) {
	cards := ttCardsNoExample("apple", "banana")
	asr := newFakeASR(asrScript{text: "造个句"})
	fallback := "老师这边还没有这个例句，你先凭印象写吧"
	llm := newFakeLLM(intentScript(intentExample, fallback))
	tm, s, log, _ := ttExampleFixture(t, cards, asr, llm, nil)
	prev := s.countOf(TypeTeacherSentence)

	ttRunIntentTurn(t, tm, s, prev)

	if llm.callCount() != 1 {
		t.Fatalf("llm calls = %d, want 1", llm.callCount())
	}
	if got := ttLastEventField(t, log, EventExampleServed, "result"); got != exampleResultFallback {
		t.Errorf("result = %q, want fallback", got)
	}
}

// TestExampleUngroundedSpeaksFallback: the resolver's structured refusal
// (no English card) degrades to the classification fallback, not to a
// context-free generation.
func TestExampleUngroundedSpeaksFallback(t *testing.T) {
	cards := ttCardsNoExample("apple", "banana")
	asr := newFakeASR(asrScript{text: "怎么用"})
	src := &fakeExampleSource{err: ErrExampleUngrounded}
	fallback := "老师这边还没有这个例句，你先凭印象写吧"
	llm := newFakeLLM(intentScript(intentExample, fallback))
	tm, s, log, _ := ttExampleFixture(t, cards, asr, llm, src)
	prev := s.countOf(TypeTeacherSentence)

	ttRunIntentTurn(t, tm, s, prev)

	if llm.callCount() != 1 {
		t.Fatalf("llm calls = %d, want 1 (ungrounded, no generation)", llm.callCount())
	}
	if got := ttSentencesSince(t, s, prev); got != fallback {
		t.Errorf("spoken reply = %q, want the fallback", got)
	}
	if got := ttLastEventField(t, log, EventExampleServed, "result"); got != exampleResultFallback {
		t.Errorf("result = %q, want fallback", got)
	}
}

// TestExampleLateGenerationDiesSilently: a generation completing after an
// interrupt (stale epoch) never speaks — neither the sentence nor the
// fallback (the processTurn post-classification discipline).
func TestExampleLateGenerationDiesSilently(t *testing.T) {
	cards := ttCardsNoExample("apple", "banana")
	asr := newFakeASR()
	src := &fakeExampleSource{ec: ExampleContext{}}
	llm := newFakeLLM(
		intentScript(intentExample, "老师这边还没有这个例句，你先凭印象写吧"),
		generationScript("比如：I eat an apple."),
	)
	tm, _, _, _ := ttExampleFixture(t, cards, asr, llm, src)

	tm.mu.Lock()
	epoch := tm.epoch
	tm.epoch = epoch + 1 // the interrupt that landed mid-generation
	target := tm.tutor.turnTarget()
	tm.mu.Unlock()

	reply, proceed := tm.exampleReply(context.Background(), epoch, intentExample, target, "fallback reply")
	if proceed {
		t.Fatal("proceed = true for a stale epoch, want the silent abort")
	}
	if reply != "" {
		t.Errorf("reply = %q, want empty on the silent abort", reply)
	}
	if llm.callCount() != 1 {
		t.Errorf("llm calls = %d, want 1 (generation ran, its reply died)", llm.callCount())
	}
}

// TestExampleCheckStageSkipsGeneration: the check stage reaches the shared
// dispatch case with WordIndex=-1 — the guard keeps it on the
// stored-information path (no second call, no seam touch).
func TestExampleCheckStageSkipsGeneration(t *testing.T) {
	cards := ttCardsNoExample("apple", "banana")
	asr := newFakeASR()
	src := &fakeExampleSource{ec: ExampleContext{}}
	llm := newFakeLLM(
		intentScript(intentExample, "老师这边还没有这个例句，你先凭印象写吧"),
		generationScript("比如：I eat an apple."),
	)
	tm, s, _, _ := ttExampleFixture(t, cards, asr, llm, src)
	prev := s.countOf(TypeTeacherSentence)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tm.mu.Lock()
	epoch := tm.epoch
	tm.mu.Unlock()
	tm.dispatchTurn(s, ctx, cancel, 1, epoch, intentExample, "老师这边还没有这个例句，你先凭印象写吧", TurnTarget{WordIndex: -1})

	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) > prev }) {
		t.Fatal("check-stage example reply never spoke")
	}
	ttAckLast(t, tm, s, 0)
	// dispatchTurn was called DIRECTLY (no classification); the guard's
	// failure mode would consume the queued scripts via exampleReply —
	// zero calls and zero resolutions prove the check stage never reaches
	// generation.
	if llm.callCount() != 0 {
		t.Errorf("llm calls = %d, want 0 (check stage never generates)", llm.callCount())
	}
	if src.callCount() != 0 {
		t.Errorf("context resolutions = %d, want 0", src.callCount())
	}
}
