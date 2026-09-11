#!/usr/bin/env bash
# phonebook.db 在线热备：用 sqlite3 .backup 做一致性快照，保留最近 7 天。
# 依赖：apt install -y sqlite3（Debian/Ubuntu）；RHEL 系为 dnf install sqlite）
set -euo pipefail

DB=/srv/phonebook/phonebook.db
BK_DIR=/var/backups/phonebook
KEEP_DAYS=7

if ! command -v sqlite3 >/dev/null 2>&1; then
  echo "缺少 sqlite3，请先安装：apt install -y sqlite3" >&2
  exit 1
fi
if [[ ! -f "$DB" ]]; then
  echo "数据库不存在：$DB" >&2
  exit 1
fi

mkdir -p "$BK_DIR"
TS=$(date +%F_%H%M%S)
OUT="$BK_DIR/phonebook-$TS.db"

# .backup 走 SQLite 在线备份 API：服务运行中也可安全执行。
sqlite3 "$DB" ".backup '$OUT'"
chmod 640 "$OUT"

find "$BK_DIR" -name 'phonebook-*.db' -mtime +"$KEEP_DAYS" -delete
echo "备份完成：$OUT"
