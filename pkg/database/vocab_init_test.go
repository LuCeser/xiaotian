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
package database

import (
	"context"
	"strings"
	"testing"

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
