#!/usr/bin/env bash
# phonebook-server 安装 / 原地升级脚本（免反代直连版）。需 root 运行。
#
# 用法：
#   ./install.sh                  # 默认 0.0.0.0:8080
#   ./install.sh 9000             # 指定端口（升级时同步更新 systemd 单元）
#   PORT=9000 ./install.sh        # 同上（环境变量形式）
#   ./install.sh --replace-db     # 危险：用同目录的 phonebook.db 覆盖服务器现有数据库
#
# 脚本会自动识别是否已安装：
#   全新安装 → 建用户/目录、装二进制、生成初始超管密码、装 systemd 与每日热备、健康检查
#   已安装   → 停服务、备份旧二进制与数据库（保留最近 5 份）、原子替换二进制、
#              更新 systemd/备份脚本、启动并健康检查；失败自动回滚旧二进制
#
# 升级时不会重新生成 phonebook.env，也不会动数据库（结构变更由程序启动时自动迁移）。
set -euo pipefail

INSTALL_DIR=/srv/phonebook
SRV_USER=phonebook
SERVICE=phonebook-server
UNIT=/etc/systemd/system/${SERVICE}.service
BACKUP_DIR="${INSTALL_DIR}/upgrade-backups"
KEEP_BACKUPS=5
SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

PORT_ARG=""
REPLACE_DB=no
for arg in "$@"; do
  case "$arg" in
    --replace-db) REPLACE_DB=yes ;;
    -h|--help)
      sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *[!0-9]*) echo "未知参数：$arg（用法：install.sh [端口] [--replace-db]）" >&2; exit 1 ;;
    *) PORT_ARG="$arg" ;;
  esac
done

# 端口优先级：命令行参数 > 环境变量 PORT > 升级时沿用现有 systemd 单元 > 8080
PORT_GIVEN=no
if [[ -n "$PORT_ARG" ]]; then
  PORT="$PORT_ARG"; PORT_GIVEN=yes
elif [[ -n "${PORT:-}" ]]; then
  PORT_GIVEN=yes
else
  PORT=8080
fi

if [[ $EUID -ne 0 ]]; then
  echo "请用 root 运行：sudo $0 [端口]" >&2
  exit 1
fi
if ! [[ "$PORT" =~ ^[0-9]+$ ]] || (( PORT < 1 || PORT > 65535 )); then
  echo "端口不合法：$PORT" >&2
  exit 1
fi

gen_pw() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -base64 15 | tr -dc 'A-Za-z0-9' | head -c 16
  elif [[ -r /dev/urandom ]]; then
    LC_ALL=C tr -dc 'A-Za-z0-9' < /dev/urandom | head -c 16
  else
    echo "install-time-$(date +%s)"
  fi
}

# 读取当前运行中服务的程序版本（用于显示 升级前 → 升级后）。
api_version() {
  curl -fsS --max-time 3 "http://127.0.0.1:${PORT}/api/version" 2>/dev/null \
    | sed -n 's/.*"appVersion":"\([^"]*\)".*/\1/p' || true
}

wait_health() {
  local i
  for i in $(seq 1 20); do
    if curl -fsS --max-time 2 "http://127.0.0.1:${PORT}/api/health" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  return 1
}

install_unit_and_backup() {
  # systemd 单元（直连 0.0.0.0:端口）
  sed "s|0.0.0.0:8080|0.0.0.0:${PORT}|" "$SELF_DIR/phonebook-server.service" > "$UNIT"
  chmod 0644 "$UNIT"

  # 每日热备（保留 7 天）
  install -o root -g root -m 0755 "$SELF_DIR/phonebook-backup.sh" /usr/local/bin/phonebook-backup.sh
  install -o root -g root -m 0644 "$SELF_DIR/phonebook-backup.service" /etc/systemd/system/phonebook-backup.service
  install -o root -g root -m 0644 "$SELF_DIR/phonebook-backup.timer" /etc/systemd/system/phonebook-backup.timer

  systemctl daemon-reload
}

prune_backups() {
  local pattern="$1"
  ls -1t "$BACKUP_DIR"/${pattern} 2>/dev/null | tail -n +$((KEEP_BACKUPS + 1)) | xargs -r rm -f
}

# ---------- 识别安装状态 ----------
MODE=fresh
if [[ -f "$INSTALL_DIR/${SERVICE}" || -f "$UNIT" ]]; then
  MODE=upgrade
fi
# 升级且未显式指定端口 → 沿用现有 systemd 单元里的端口，避免把 9000 改回 8080
if [[ "$MODE" == upgrade && "$PORT_GIVEN" == no ]]; then
  EXISTING_PORT="$(sed -n 's/.*0\.0\.0\.0:\([0-9]\+\).*/\1/p' "$UNIT" 2>/dev/null | head -1 || true)"
  if [[ -n "$EXISTING_PORT" ]]; then
    PORT="$EXISTING_PORT"
    echo "    未指定端口，沿用现有配置：$PORT"
  fi
fi
if ! [[ "$PORT" =~ ^[0-9]+$ ]] || (( PORT < 1 || PORT > 65535 )); then
  echo "端口不合法：$PORT" >&2
  exit 1
fi

# 1) 目录与账号
mkdir -p "$INSTALL_DIR"
if ! id "$SRV_USER" &>/dev/null; then
  useradd --system --no-create-home --shell /usr/sbin/nologin "$SRV_USER"
fi
# 目录必须归服务用户所有，否则服务建/写 phonebook.db 及其 -wal/-shm 时
# 会报 “unable to open database file”(SQLITE_CANTOPEN=14)。
chown "$SRV_USER:$SRV_USER" "$INSTALL_DIR"
chmod 0750 "$INSTALL_DIR"

if [[ "$MODE" == upgrade ]]; then
  echo "==> 检测到已安装（$INSTALL_DIR/${SERVICE}），执行原地升级"
  OLD_VER="$(api_version)"; OLD_VER="${OLD_VER:-未知}"
  echo "    升级前版本：$OLD_VER"

  systemctl stop "$SERVICE" 2>/dev/null || true

  # 备份旧二进制与数据库（服务已停，直接拷贝即一致快照）
  TS="$(date +%Y%m%d-%H%M%S)"
  mkdir -p "$BACKUP_DIR"
  cp -p "$INSTALL_DIR/${SERVICE}" "$BACKUP_DIR/${SERVICE}.$TS"
  if [[ -f "$INSTALL_DIR/phonebook.db" ]]; then
    cp -p "$INSTALL_DIR/phonebook.db" "$BACKUP_DIR/phonebook.db.$TS"
  fi
  chown -R "$SRV_USER:$SRV_USER" "$BACKUP_DIR"
  chmod 0700 "$BACKUP_DIR"
  prune_backups "${SERVICE}.*"
  prune_backups "phonebook.db.*"
  echo "    已备份旧二进制/数据库 → $BACKUP_DIR（保留最近 $KEEP_BACKUPS 份）"

  # 原子替换二进制（先装成 .new 再 mv，避免替换瞬间被打断留下半截文件）
  install -o "$SRV_USER" -g "$SRV_USER" -m 0755 "$SELF_DIR/${SERVICE}" "$INSTALL_DIR/${SERVICE}.new"
  mv -f "$INSTALL_DIR/${SERVICE}.new" "$INSTALL_DIR/${SERVICE}"

  # 显式要求时才覆盖数据库（防止误把安装包旁的 .db 覆盖生产库）
  if [[ "$REPLACE_DB" == yes && -f "$SELF_DIR/phonebook.db" ]]; then
    install -o "$SRV_USER" -g "$SRV_USER" -m 0644 "$SELF_DIR/phonebook.db" "$INSTALL_DIR/phonebook.db"
    echo "    ⚠ 已按 --replace-db 覆盖服务器数据库（旧库已备份）"
  fi
else
  echo "==> 未检测到已安装，执行全新安装"
  install -o "$SRV_USER" -g "$SRV_USER" -m 0755 "$SELF_DIR/${SERVICE}" "$INSTALL_DIR/${SERVICE}"

  # 全新安装：同目录带 phonebook.db 则直接导入（首次迁移）
  if [[ -f "$SELF_DIR/phonebook.db" ]]; then
    install -o "$SRV_USER" -g "$SRV_USER" -m 0644 "$SELF_DIR/phonebook.db" "$INSTALL_DIR/phonebook.db"
    echo "    已导入同目录的 phonebook.db（保留既有账号与数据）"
  fi
fi

# 2) 环境文件（仅缺失时生成；升级不动它）
ADMIN_PW=""
if [[ ! -f "$INSTALL_DIR/phonebook.env" ]]; then
  ADMIN_PW="$(gen_pw)"
  umask 077
  cat > "$INSTALL_DIR/phonebook.env" <<EOF
PB_ADMIN_USER=admin
PB_ADMIN_PASSWORD=$ADMIN_PW
EOF
  chown "$SRV_USER:$SRV_USER" "$INSTALL_DIR/phonebook.env"
  chmod 0600 "$INSTALL_DIR/phonebook.env"
fi

# 3) systemd 单元 + 每日热备
install_unit_and_backup
systemctl enable "$SERVICE" >/dev/null 2>&1 || true
# 不因 start 失败直接退出（要留给后面的健康检查与回滚逻辑处理）
systemctl start "$SERVICE" || true
systemctl enable --now phonebook-backup.timer >/dev/null 2>&1 || true

# 4) 健康检查；升级失败自动回滚
if ! wait_health; then
  if [[ "$MODE" == upgrade ]]; then
    echo "升级后健康检查未通过，正在回滚到上一版本…" >&2
    systemctl stop "$SERVICE" 2>/dev/null || true
    LAST="$(ls -1t "$BACKUP_DIR"/${SERVICE}.* 2>/dev/null | head -1 || true)"
    if [[ -n "$LAST" ]]; then
      install -o "$SRV_USER" -g "$SRV_USER" -m 0755 "$LAST" "$INSTALL_DIR/${SERVICE}"
      systemctl start "$SERVICE" || true
      if wait_health; then
        echo "已回滚到旧版本（$LAST）并恢复正常。" >&2
      else
        echo "回滚后仍未就绪，请查看：journalctl -u $SERVICE -e" >&2
      fi
    else
      echo "未找到可回滚的备份，请查看：journalctl -u $SERVICE -e" >&2
    fi
  else
    echo "警告：服务未在 20 秒内就绪，请查看日志：journalctl -u $SERVICE -e" >&2
  fi
  exit 1
fi

NEW_VER="$(api_version)"; NEW_VER="${NEW_VER:-未知}"

echo
echo "=============================================================="
if [[ "$MODE" == upgrade ]]; then
  echo " phonebook-server 升级完成"
  echo "   版本：$OLD_VER → $NEW_VER"
  echo "   旧版本/数据库备份：$BACKUP_DIR（保留最近 $KEEP_BACKUPS 份）"
  echo "   账号与数据未变动（数据库结构变更已在启动时自动迁移）"
else
  echo " phonebook-server 安装完成（版本 $NEW_VER）"
fi
echo "   访问地址：  http://<本机IP>:$PORT"
if [[ "$MODE" == fresh && -n "$ADMIN_PW" ]]; then
  echo "   初始超管：  admin"
  echo "   初始密码：  $ADMIN_PW"
  echo "   （首次登录会被强制要求改成自己的密码）"
fi
echo "   每日热备：   /var/backups/phonebook/（保留 7 天）"
echo "   管理/运维：  journalctl -u $SERVICE -f"
echo "   忘记密码：  sudo systemctl stop $SERVICE && \\"
echo "                 PB_RECOVER_PASSWORD='新密码' sudo -u $SRV_USER $INSTALL_DIR/$SERVICE recover-admin -db $INSTALL_DIR/phonebook.db"
echo "=============================================================="
echo
echo " 提示：请放行防火墙端口 $PORT/tcp（ufw 示例：ufw allow $PORT/tcp）。"
echo "      本版为 HTTP 直连（未配置反代），如部署到公网建议再加一层 HTTPS 反代。"
