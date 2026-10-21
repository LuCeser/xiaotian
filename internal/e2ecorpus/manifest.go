// Package e2ecorpus holds the milestone-1 live-input corpus: the text
// manifest kept in the repo (tests/e2e-m1/corpus/manifest.json) and the
// cache-key scheme for the synthesized audio kept OUTSIDE the repo
// (XIAOTIAN_E2E_AUDIO_DIR). 语料与预期结果分开维护：the manifest pins text,
// scene and the expected classroom action BEFORE any recognition runs, so
// expectations are never derived from ASR output. Audio never enters the
// repo, and only child-INPUT audio is ever cached — never ASR/LLM/OCR
// responses or teacher TTS (计划第 4 节).
package e2ecorpus

import (
	"encoding/json"
	"fmt"
	"os"
)

// 语料组 (计划第 4 节首批语料表).
const (
	GroupConfirm          = "confirm"           // 明确完成
	GroupDeny             = "deny"              // 否定／等待
	GroupRepeat           = "repeat"            // 重读
	GroupSubject          = "subject"           // 选科
	GroupSubjectAmbiguous = "subject-ambiguous" // 不明确选科
	GroupAnswer           = "answer"            // 答案与完成组合
	GroupVague            = "vague"             // 含糊／上下文
	GroupState            = "state"             // 状态对照
)

// 预期动作，预先标注 (计划第 4 节：不能从 ASR 输出推导期望动作).
const (
	ExpectAdvance        = "advance"          // 等待当前词完成时推进一次
	ExpectHold           = "hold"             // 不推进
	ExpectRepeat         = "repeat"           // 重读，不推进
	ExpectSelect         = "select-subject"   // 选科阶段绑定学科（Subject 字段）
	ExpectKeepAsking     = "keep-asking"      // 保持询问，不擅自选科
	ExpectAnswer         = "answer"           // 单独答案输入：不推进、不揭示答案
	ExpectClarify        = "clarify"          // 含糊：不凭局部词判断完成
	ExpectNoPlainAdvance = "no-plain-advance" // 非默写状态不套用普通推进规则
)

// 课堂状态：同一句话可用于多个状态，状态决定预期动作.
const (
	SceneDictation     = "dictation"      // 默写中
	SceneSubjectSelect = "subject-select" // 选科询问中
	ScenePaused        = "paused"         // 暂停中
	SceneChecking      = "checking"       // 检查中
)

// Subject values for ExpectSelect.
const (
	SubjectChinese = "chinese"
	SubjectEnglish = "english"
)

var knownGroups = map[string]bool{
	GroupConfirm: true, GroupDeny: true, GroupRepeat: true, GroupSubject: true,
	GroupSubjectAmbiguous: true, GroupAnswer: true, GroupVague: true, GroupState: true,
}

var knownExpects = map[string]bool{
	ExpectAdvance: true, ExpectHold: true, ExpectRepeat: true, ExpectSelect: true,
	ExpectKeepAsking: true, ExpectAnswer: true, ExpectClarify: true, ExpectNoPlainAdvance: true,
}

var knownScenes = map[string]bool{
	SceneDictation: true, SceneSubjectSelect: true, ScenePaused: true, SceneChecking: true,
}

// Entry is one utterance in the corpus.
type Entry struct {
	ID      string `json:"id"`
	Group   string `json:"group"`
	Text    string `json:"text"`
	Scene   string `json:"scene"`
	Expect  string `json:"expect"`
	Subject string `json:"subject,omitempty"` // set iff Expect == ExpectSelect
}

// Load reads and validates a corpus manifest ({"version":1,"entries":[...]}).
// Validation fails the whole file on the first bad entry — a typo in the
// manifest must never silently drop a case from the suite.
func LoadManifest(path string) ([]Entry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("e2ecorpus: read manifest: %w", err)
	}
	var m struct {
		Entries []Entry `json:"entries"`
		Version int     `json:"version"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("e2ecorpus: parse manifest %s: %w", path, err)
	}
	if m.Version != 1 {
		return nil, fmt.Errorf("e2ecorpus: manifest %s version = %d, want 1", path, m.Version)
	}
	if len(m.Entries) == 0 {
		return nil, fmt.Errorf("e2ecorpus: manifest %s has no entries", path)
	}
	ids := make(map[string]bool, len(m.Entries))
	for i, e := range m.Entries {
		if err := e.validate(); err != nil {
			return nil, fmt.Errorf("e2ecorpus: manifest %s entry[%d]: %w", path, i, err)
		}
		if ids[e.ID] {
			return nil, fmt.Errorf("e2ecorpus: manifest %s: duplicate id %q", path, e.ID)
		}
		ids[e.ID] = true
	}
	return m.Entries, nil
}

// validate checks one entry against the enums above.
func (e Entry) validate() error {
	switch {
	case e.ID == "":
		return fmt.Errorf("empty id")
	case e.Text == "":
		return fmt.Errorf("id %s: empty text", e.ID)
	case !knownGroups[e.Group]:
		return fmt.Errorf("id %s: unknown group %q", e.ID, e.Group)
	case !knownExpects[e.Expect]:
		return fmt.Errorf("id %s: unknown expect %q", e.ID, e.Expect)
	case !knownScenes[e.Scene]:
		return fmt.Errorf("id %s: unknown scene %q", e.ID, e.Scene)
	}
	if e.Expect == ExpectSelect {
		if e.Subject != SubjectChinese && e.Subject != SubjectEnglish {
			return fmt.Errorf("id %s: expect %s requires subject %q or %q, got %q",
				e.ID, ExpectSelect, SubjectChinese, SubjectEnglish, e.Subject)
		}
		return nil
	}
	if e.Subject != "" {
		return fmt.Errorf("id %s: subject %q set but expect is %s, not %s",
			e.ID, e.Subject, e.Expect, ExpectSelect)
	}
	return nil
}
