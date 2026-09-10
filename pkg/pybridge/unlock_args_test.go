package pybridge

import (
	"reflect"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// The unlock argv contract: category/unit sweep by default, scoped --ids
// release when IDs are present (Ticket 07 — session isolation: one student
// exiting must not unlock another's in-flight cards).
func TestUnlockArgs(t *testing.T) {
	cfg := infra.PythonConfig{VocabScript: "vocab_manager.py", VocabDBPath: "vocab.db"}

	tests := []struct {
		name string
		opts UnlockOpts
		want []string
	}{
		{
			name: "category sweep (testing aid path)",
			opts: UnlockOpts{Category: "chinese", Unit: "3"},
			want: []string{"vocab_manager.py", "--db", "vocab.db", "unlock",
				"--category", "chinese", "--unit", "3"},
		},
		{
			name: "scoped ids take precedence over category/unit",
			opts: UnlockOpts{Category: "chinese", Unit: "3", IDs: []int64{7, 8, 9}},
			want: []string{"vocab_manager.py", "--db", "vocab.db", "unlock",
				"--ids", "[7,8,9]"},
		},
		{
			name: "scoped ids alone",
			opts: UnlockOpts{IDs: []int64{42}},
			want: []string{"vocab_manager.py", "--db", "vocab.db", "unlock",
				"--ids", "[42]"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := unlockArgs(cfg, tt.opts)
			if err != nil {
				t.Fatalf("unlockArgs() error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("unlockArgs() = %v, want %v", got, tt.want)
			}
		})
	}
}
