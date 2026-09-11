# phonebook-server 自动安装包（免反代）

适用：**Linux amd64 服务器**，一条命令自动装好并启动，无需配反代/域名/证书。
一个进程对外提供：通讯录只读 API + 单位/联系人等管理 API + 内置 Web 管理页。

## 包内文件

| 文件 | 作用 |
|---|---|
| `phonebook-server` | Linux amd64 静态二进制（已编好，服务器不用装 Go） |
| `install.sh` | 自动安装脚本（唯一要执行的） |
| `phonebook-server.service` | systemd 单元（直连 `0.0.0.0:端口`，被 install.sh 使用） |
| `phonebook-backup.sh` / `.service` / `.timer` | 每日热备，保留 **7 天** |

## 安装步骤（3 步）

```bash
# 1) 拷到服务器任意目录（二进制约 18MB）
scp phonebook-server_auto-install_linux-amd64.tar.gz root@服务器IP:/root/

# 2) 解压
ssh root@服务器IP
tar xzf phonebook-server_auto-install_linux-amd64.tar.gz
cd phonebook-server_installer      # 解压出的目录名，见包实际目录

# 3) 安装 / 升级（root；可选端口参数，默认 8080）
./install.sh                 # 未安装→全新安装；已安装→原地升级
./install.sh 9000            # 指定端口
./install.sh --replace-db    # 危险：用同目录 phonebook.db 覆盖服务器现有库
```

脚本自动：建专用用户 `phonebook` → 装二进制/库 → 写 systemd 服务并立即启动 → 装每日热备 → 健康检查通过后打印访问地址。

## 自动识别：全新安装 vs 原地升级

脚本会检测 `/srv/phonebook/phonebook-server` 或 systemd 单元是否已存在：

| | 全新安装 | 原地升级 |
|---|---|---|
| 二进制 | 直接安装 | 先备份旧二进制与数据库 → **原子替换**（`.new` → `mv`） |
| 数据库 | 同目录有 `phonebook.db` 才导入 | **不动**（结构变更由程序启动时自动迁移）；仅 `--replace-db` 才覆盖 |
| `phonebook.env` | 生成并打印初始密码 | **保留原样**，不重新生成 |
| 端口 | 参数/环境变量，默认 8080 | 未显式指定时**沿用现有单元里的端口** |
| 失败处理 | 打印日志提示 | **自动回滚到上一版本**并重启 |

- 备份位置：`/srv/phonebook/upgrade-backups/`（旧二进制 + 旧数据库各保留最近 **5** 份）；
- 升级开始与结束都会打印版本：`升级前版本 xxxx → 升级后版本 xxxx`；
- 回滚：健康检查 20 秒未通过 → 自动换回最近一次备份的二进制并重启。

## 两种数据初始化方式

- **全新安装**（同目录**没有** `phonebook.db`）：脚本自动生成初始超管密码并打印，账号 `admin`，首次登录强制改密。
- **带数据迁移**：先把你现有的 `phonebook.db`（如 `phonebook-server/phonebook.db`，schema v7）拷到解压目录里再 `./install.sh`，会原样保留全部账号/单位/联系人/日志。

```bash
scp phonebook.db root@服务器IP:/root/phonebook-server_installer/
```

## 装完做什么

- 浏览器打开 `http://服务器IP:端口`，用 `admin` + 打印的初始密码登录。
- 防火墙放行该端口：`ufw allow 8080/tcp`（用端口替换）。
- 客户端打包时指向它：`flutter build apk --release --dart-define=API_BASE_URL=http://服务器IP:端口`

## 运维

```bash
systemctl status phonebook-server            # 状态
journalctl -u phonebook-server -f            # 日志
systemctl restart phonebook-server           # 重启
ls /var/backups/phonebook/                   # 每日备份（保留 7 天）
```

忘记 admin 密码（应急恢复，服务器本机执行）：

```bash
systemctl stop phonebook-server
PB_RECOVER_PASSWORD='新密码' /srv/phonebook/phonebook-server recover-admin -db /srv/phonebook/phonebook.db
systemctl start phonebook-server
```

## 说明 / 注意

- **本版为 HTTP 直连、未配反代**：管理员登录密码会明文经过网络。用于**内网/可信环境**没问题；如果公网暴露强烈建议再套一层 HTTPS（Caddy/Nginx），把服务的监听改回 `127.0.0.1`。数据在 SQLite 里，放服务器本地磁盘即可。
- **升级**：解压新版本安装包后直接再跑一次 `./install.sh` 即可——脚本自动识别已安装、备份旧版本（`/srv/phonebook/upgrade-backups/`）、原子替换二进制、健康检查失败自动回滚；数据库不动（结构变更启动时自动迁移）。要换库才用 `--replace-db`。
