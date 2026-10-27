// vocab_init_test.go 锁定 issue #78 的服务端失败模式。
//
// issue #78：全新安装后 /api/v1/units 返回 500（"failed to list units"），
// 根因是 cards 等 Python 侧表只在 Python 子进程首次运行时由
// vocab_manager.py 的 init_db 创建，而 GET /api/v1/units 是 Go 直读
// SQLite（CardRepository.CountByUnit，D-12 不起 Python 子进程）。
//
// 修复（main 3ac8f8f，安装器路径）：vocab_manager.py 提供显式幂等 `init`
// 子命令，install.ps1 第 6b 步在启动服务前调用一次。
//
// TestCountByUnit_MissingCardsTable 是纯 Go 复现测试：只建 Go 侧表的空库
// （等价于全新安装首启、安装器 init 步骤未跑或降级时的库状态）上，
// CountByUnit 必须报 "no such table: cards"。它长期为绿，锁定的是症状
// 契约：若有人改用「Go 侧悄悄建 Python 表」（违反 D-08 写分离边界）或
// 「吞掉缺表错误返回空列表」（掩盖问题）来修这条路径，本测试变红。
//
// TestVocabInitBootstrapsGoReadableSchema 是跨语言集成测试，验证修复机制
// 本身：对临时空库执行真实的 `vocab_manager.py --db <path> init`（与
// install.ps1 第 6b 步完全相同的调用）后，Go 读路径恢复正常，且对已有
// 数据的库重复执行不丢数据（幂等）。去掉 init 子命令后它变红（argparse
// 拒绝未知子命令，退出码非 0）。需要仓库标准 venv（third_party/python/
// .venv，见 Makefile py-bootstrap）或 HTA_PYTHON_INTERPRETER 指向的解释
// 器；都没有时跳过 —— CI 的 build-and-test 作业不装 venv，安装器侧由
// tests/test-install.js 的断言覆盖。
package database

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// TestCountByUnit_MissingCardsTable 复现 issue #78 的服务端症状：
// 全新安装首启后（Go 侧 Migrate 已跑、Python 侧 init 未跑），
// /api/v1/units 的底层读 CountByUnit 报 no such table: cards。
func TestCountByUnit_MissingCardsTable(t *testing.T) {
	// newTestDB 只建 Go 侧表（sessions/session_events），正是破损安装
	// 首启时的库状态：4096 字节空库 + Go 自己的表，没有任何 Python 表。
	db := newTestDB(t)
	repo, err := NewCardRepository(db, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewCardRepository: %v", err)
	}

	_, err = repo.CountByUnit(context.Background(), "english")
	if err == nil {
		t.Fatal("CountByUnit on a DB without the Python-owned cards table succeeded, want the no-such-table error (issue #78 symptom)")
	}
	if !strings.Contains(err.Error(), "no such table: cards") {
		t.Fatalf("CountByUnit error = %q, want it to contain %q", err.Error(), "no such table: cards")
	}
}

// repoRoot 返回仓库根目录：从测试进程的工作目录（go test 的包目录
// pkg/database）逐级向上查找 go.mod。不用 runtime.Caller —— CI 设
// GOFLAGS=-trimpath 时 Caller 返回模块路径（github.com/xiaotian/…）而非
// 源码路径，随后 go.mod 检查必然失败。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root not found: walked up from the test working directory without finding go.mod")
		}
		dir = parent
	}
}

// vocabTestInterpreter 定位带 fsrs 依赖的解释器：优先 HTA_PYTHON_INTERPRETER
// （pybridge e2e 的既有惯例），其次仓库标准 venv（Makefile py-bootstrap 的
// 位置）。找不到则返回 ""，调用方跳过 —— 决不回落到系统 python3：
// vocab_manager.py 在模块顶层 import fsrs，裸解释器必然失败，会制造假红。
func vocabTestInterpreter(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("HTA_PYTHON_INTERPRETER"); p != "" {
		if _, err := exec.LookPath(p); err == nil {
			return p
		}
		t.Skipf("HTA_PYTHON_INTERPRETER=%q is not executable", p)
	}
	root := repoRoot(t)
	for _, cand := range []string{
		filepath.Join(root, "third_party", "python", ".venv", "bin", "python"),
		filepath.Join(root, "third_party", "python", ".venv", "Scripts", "python.exe"),
	} {
		if info, err := os.Stat(cand); err == nil && !info.IsDir() {
			return cand
		}
	}
	return ""
}

// runVocabInit 以 install.ps1 第 6b 步的相同方式执行
// `vocab_manager.py --db <dbPath> init`，断言退出码 0。
func runVocabInit(t *testing.T, interpreter, dbPath string) {
	t.Helper()
	root := repoRoot(t)
	script := filepath.Join(root, "third_party", "python", "vocab_manager.py")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, interpreter, script, "--db", dbPath, "init")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("vocab_manager.py init failed: %v\noutput:\n%s", err, out)
	}
}

// TestVocabInitBootstrapsGoReadableSchema 验证 issue #78 修复机制：
// 安装器执行的 `vocab_manager.py init` 产出的库，Go 的 /api/v1/units
// 读路径（CountByUnit）可直接查询；且对已有数据的库重复执行不丢数据。
func TestVocabInitBootstrapsGoReadableSchema(t *testing.T) {
	interpreter := vocabTestInterpreter(t)
	if interpreter == "" {
		t.Skip("no Python venv with fsrs found (make py-bootstrap) and HTA_PYTHON_INTERPRETER unset — skipping the cross-language init test")
	}

	dbPath := filepath.Join(t.TempDir(), "vocab.db")

	// 修复前状态：空库（无任何表）。init 后四张 Python 表必须存在。
	runVocabInit(t, interpreter, dbPath)

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open(%s): %v", dbPath, err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var tables []string
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type = 'table'")
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	for rows.Next() {
		var name string
		err = rows.Scan(&name)
		if err != nil {
			t.Fatalf("scan sqlite_master: %v", err)
		}
		tables = append(tables, name)
	}
	err = rows.Err()
	if err != nil {
		t.Fatalf("iterate sqlite_master: %v", err)
	}
	_ = rows.Close()
	for _, want := range []string{"cards", "textbooks", "review_logs", "card_assignments"} {
		found := false
		for _, got := range tables {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("after init, table %q missing (have %v)", want, tables)
		}
	}

	// 建表后正常：CountByUnit（GET /api/v1/units 的底层读）不再报错，
	// 空库返回空非 nil 列表。
	repo, err := NewCardRepository(db, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewCardRepository: %v", err)
	}
	units, err := repo.CountByUnit(context.Background(), "english")
	if err != nil {
		t.Fatalf("CountByUnit after init: %v", err)
	}
	if units == nil || len(units) != 0 {
		t.Fatalf("CountByUnit after init on empty table = %v, want empty non-nil slice", units)
	}

	// 幂等/不丢数据：已有数据的库重复执行 init（重装场景），行数不变，
	// 计数照常读出。
	_, err = db.Exec(
		"INSERT INTO cards (word, meaning, example, unit, category, fsrs_card) VALUES ('apple', '苹果', '', 'unitA', 'english', '')",
	)
	if err != nil {
		t.Fatalf("seed existing card: %v", err)
	}
	runVocabInit(t, interpreter, dbPath)

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM cards").Scan(&count)
	if err != nil {
		t.Fatalf("count cards after re-init: %v", err)
	}
	if count != 1 {
		t.Fatalf("re-running init changed data: cards count = %d, want 1 (init must be idempotent)", count)
	}
	units, err = repo.CountByUnit(context.Background(), "english")
	if err != nil {
		t.Fatalf("CountByUnit after re-init: %v", err)
	}
	if len(units) != 1 || units[0].Unit != "unitA" || units[0].Count != 1 {
		t.Fatalf("CountByUnit after re-init = %+v, want [{unitA 1}]", units)
	}
}
