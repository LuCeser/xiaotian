#!/bin/bash
# 重置所有词汇为Learning状态，使它们立即可用于测试

echo "=== 重置词汇状态为Learning ==="

# 1. 获取所有词汇ID
VOCAB_IDS=$(third_party/python/.venv/Scripts/python.exe third_party/python/vocab_manager.py list | python -c "import sys, json; data=json.load(sys.stdin); print(' '.join([str(str(item['id'])) for item in data]))")

if [ -z "$VOCAB_IDS" ]; then
    echo "❌ 没有找到词汇"
    exit 1
fi

echo "📝 找到词汇ID: $VOCAB_IDS"

# 2. 通过SQLite直接更新状态
DB_PATH="./.hta-data/vocab.db"

if [ ! -f "$DB_PATH" ]; then
    echo "❌ 数据库不存在: $DB_PATH"
    exit 1
fi

echo "🔄 更新FSRS状态为Learning (state=1)..."
sqlite3 "$DB_PATH" <<EOF
-- 更新所有词汇为Learning状态
UPDATE vocabulary SET state = 1 WHERE state != 1;

-- 设置due时间为当前时间-1分钟（立即可用）
UPDATE vocabulary SET due = datetime('now', '-1 minute');

-- 清除fetch锁
UPDATE vocabulary SET fetch_lock = NULL WHERE fetch_lock IS NOT NULL;
EOF

echo "✅ 状态更新完成"

# 3. 验证结果
echo ""
echo "=== 验证结果 ==="
third_party/python/.venv/Scripts/python.exe third_party/python/vocab_manager.py stats
echo ""
echo "📋 到期词汇列表:"
third_party/python/.venv/Scripts/python.exe third_party/python/vocab_manager.py due --limit 5

echo ""
echo "✅ 重置完成！现在可以测试了"
