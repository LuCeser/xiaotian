package e2ecorpus

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Synthesizer produces the source WAV bytes for one corpus text. The
// prepare command binds it to the real MIMO TTS; tests bind a fake — the
// library itself never talks to a provider.
type Synthesizer interface {
	SynthesizeWAV(ctx context.Context, text string, p SynthParams) ([]byte, error)
}

// PrepareOptions bounds one prepare run (计划第 6 节额度控制：只处理缺失
// 项，设每次新增数量与总字符上限). Limit and MaxChars ≤ 0 mean uncapped.
type PrepareOptions struct {
	Now      func() time.Time // timestamp recorded in metadata
	Dir      string           // cache dir (outside the repo, XIAOTIAN_E2E_AUDIO_DIR)
	Limit    int              // max items synthesized per run
	MaxChars int              // max total synthesized characters per run
}

// PrepareReport is the evidence of what one run did — synthesized IDs, what
// stayed cached (命中不生成), what the budget skipped, and quota spent.
type PrepareReport struct {
	Synthesized   []string // entry IDs newly synthesized and saved
	BudgetSkipped []string // missing entries the count/char caps left for a later run
	Cached        int      // entries already cached; no synthesis attempted
	Chars         int      // characters actually synthesized this run
}

// Prepare synthesizes only the manifest entries whose cache key misses, in
// manifest order, stopping at the first item that would exceed the item or
// character cap (predictable order: the next run picks up where this one
// stopped) and at the first provider/save failure. Already-cached entries
// are never re-synthesized: a present entry must also verify (Load), and a
// damaged or torn one is a hard error that keeps the files as evidence —
// prepare never overwrites existing entries; refreshing one is an explicit
// act outside this command (计划第 4 节：命中不生成、默认只读缓存；第 6 节：
// 遇到首个硬失败停止新增并保留证据).
func Prepare(ctx context.Context, entries []Entry, p SynthParams, synth Synthesizer, opts PrepareOptions) (PrepareReport, error) {
	var report PrepareReport
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}

	var missing []pendingEntry
	seenKey := map[string]bool{}
	for _, e := range entries {
		key := Key(e, p)
		if seenKey[key] {
			continue // two entries sharing text share one synthesis
		}
		seenKey[key] = true
		switch classifyCacheEntry(opts.Dir, key) {
		case cacheHit:
			// Presence alone is not a hit: a truncated file or broken
			// sidecar must fail the run instead of passing as cached.
			if _, err := Load(opts.Dir, key); err != nil {
				return report, fmt.Errorf("e2ecorpus: cached entry %s (%q, key %s) is damaged: %w — nothing re-synthesized or deleted; inspect or remove the files by hand (refresh is explicit)",
					e.ID, e.Text, key, err)
			}
			report.Cached++
		case cachePartial:
			// Torn entries are damage, not a fresh miss — re-synthesizing
			// here would silently overwrite the evidence.
			present, absent := entryFileNames(opts.Dir, key)
			return report, fmt.Errorf("e2ecorpus: cache entry %s (%q, key %s) is incomplete: present [%s], missing [%s] — not treated as a fresh miss; nothing re-synthesized or deleted; inspect or remove the files by hand (refresh is explicit)",
				e.ID, e.Text, key, strings.Join(present, ", "), strings.Join(absent, ", "))
		default:
			missing = append(missing, pendingEntry{entry: e, key: key})
		}
	}

	synthesized, budgetSkipped, chars, err := synthesizeMissing(ctx, missing, p, synth, opts, now)
	report.Synthesized = synthesized
	report.BudgetSkipped = budgetSkipped
	report.Chars = chars
	return report, err
}

// pendingEntry is a missing cache item awaiting synthesis.
type pendingEntry struct {
	entry Entry
	key   string
}

// synthesizeMissing walks the missing list applying the caps. The first
// entry that no longer fits stops the run: everything after it is recorded
// as budget-skipped, keeping the spend order deterministic.
func synthesizeMissing(ctx context.Context, missing []pendingEntry, p SynthParams, synth Synthesizer, opts PrepareOptions, now func() time.Time) ([]string, []string, int, error) {
	var synthesized, skipped []string
	chars := 0
	for i, m := range missing {
		if opts.Limit > 0 && len(synthesized) >= opts.Limit ||
			opts.MaxChars > 0 && chars+len([]rune(m.entry.Text)) > opts.MaxChars {
			for _, rest := range missing[i:] {
				skipped = append(skipped, rest.entry.ID)
			}
			return synthesized, skipped, chars, nil
		}
		wav, err := synth.SynthesizeWAV(ctx, m.entry.Text, p)
		if err != nil {
			return synthesized, skipped, chars, fmt.Errorf("e2ecorpus: synthesize %s (%q): %w", m.entry.ID, m.entry.Text, err)
		}
		if _, err := Save(opts.Dir, m.entry, p, wav, now()); err != nil {
			return synthesized, skipped, chars, fmt.Errorf("e2ecorpus: save %s: %w", m.entry.ID, err)
		}
		synthesized = append(synthesized, m.entry.ID)
		chars += len([]rune(m.entry.Text))
	}
	return synthesized, skipped, chars, nil
}
