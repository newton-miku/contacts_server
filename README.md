# phonebook-server

通讯录后端（Go + SQLite，modernc.org/sqlite 无 CGO）。提供：

- **公开只读 API**：客户端拿「数据版本号 + 联系人快照」（gzip）；匿名=全量；
- **管理页**（Vue 3 + Element Plus，侧边栏；产物内嵌进可执行文件）；
- **单位级多管理员权限体系**：超级管理员（super）+ 单位管理员（unit），
  单位管理员只能查看/修改其绑定单位的联系人（服务端强校验）；
- **审计日志**（登录/改密/账号管理/联系人变更/单位维护），超管可筛选查询；
- **完整账号校验**：bcrypt + SHA-256 令牌（Cookie `pb_session` 或 `Authorization: Bearer`）。

## 运行

```bash
# 直接跑源码（默认监听 :8080，数据库文件 phonebook.db）
go run . -addr :8080 -db phonebook.db

# 或编译成单文件后运行（管理页已内嵌，可拷到无 Node/网络的环境跑）
go build -o phonebook-server.exe .
./phonebook-server.exe -addr :8080 -db phonebook.db
```

启动日志会打印监听地址与**初始管理员账号**（首次运行、users 表为空时自动创建，角色=超级管理员）。
首次登录/被重置密码的账号都会被**强制要求先改密**才能做其它操作。

### 初始管理员
| 配置 | 环境变量 | 默认 |
| --- | --- | --- |
| 用户名 | `PB_ADMIN_USER` | `admin` |
| 密码 | `PB_ADMIN_PASSWORD` | **无默认值**：未提供时首次启动随机生成并在控制台打印（首次登录强制改密） |

种子只发生一次；此后改环境变量不影响已建账号。上线请务必用环境变量指定强密码。
管理页：浏览器打开 `http://localhost:8080`（侧边栏按角色显隐「单位管理/管理员管理/系统日志」仅超管可见）。

## 数据模型与权限

- 联系人字段：**姓名、手机号、手机2、座机、传真、单位、职务、邮箱**（号码与可选字段允许 null）；
- **姓名不含空格**：新增/修改/导入时一律自动删除姓名里的全部空白（半角/全角空格、制表符、
  不换行空格、零宽空格等），`张 三` 与 `张三` 是同一个人——导入以「单位+姓名」为关键字判重，
  不这样归一就会因为一个空格建出重复记录。升级时（schema v10）会把历史姓名里的空白一次性去掉。
- `units` 预定义单位表（超管维护，改单位名不影响既有联系人）；
- `users.role`：`super` 超级管理员（管单位/管理员/日志，可管全部数据）｜`unit` 单位管理员（可绑 1..N 个单位）；
- `logs`：审计日志；`meta.data_version`：数据版本（脏标记 + 静默窗口合并发布，见下）。

范围规则：匿名客户端与 super 可见全部；unit 只见/只改其绑定单位。**CSV 不设独立权限**，
导入/导出跟随上述同一数据范围（unit 只能导出/导入自己单位，super 全量）。

## 接口

公开（客户端，无需登录）
- `GET /api/version` → `{"version":N,"updatedAt":"...","pending":false,"quietMinutes":15,"publishDueAt":"..."}`
  （几十字节的**纯读**接口；`pending=true` 表示还有改动没发布，`publishDueAt` 是预计自动发布时间）
- `GET /api/contacts` → `{"version":N,"updatedAt":"...","contacts":[{id,displayName,phoneNumber,unitId,unit,email,jobTitle}]}`
  （带有效登录会话时按角色过滤；无会话=匿名全量）

数据版本发布（管理员，super/unit/root）
- `POST /api/version/publish` → `{"ok":true,"version":N,"published":true}`
  **手动立即发布**：把待发布的改动合并成一次版本递增，手机端下次探测即拉全量；
  没有待发布改动时 `published=false`、版本号不变（幂等，可放心连点）

身份
- `POST /api/auth/login` `{username,password}` → `{token,user}` + Set-Cookie（7 天；user 带 role，unit 还带绑定单位）
- `GET  /api/auth/me` / `POST /api/auth/logout` / `POST /api/auth/password` `{oldPassword,newPassword}`

联系人写（super 或 unit，须已过强制改密；跨自己数据范围写返回 403）
- `POST   /api/contacts` / `PUT /api/contacts/{id}` / `DELETE /api/contacts/{id}`
- `GET    /api/contacts/template`：**空的导入模板**（只有表头、没有数据行，UTF-8+BOM，Excel 双击不乱码），
  管理页「下载导入模板」按钮即调它；填好后走下面的导入
- `GET    /api/contacts/export`：CSV（UTF-8 BOM；表头 `姓名,手机号,手机2,座机,传真,单位,职务,邮箱`）
- `POST   /api/contacts/import`：multipart `file`；以「**单位+姓名**」为关键字，命中唯一则覆盖、否则新增；
  整批一个事务，带行号错误；**超级管理员可一次性导入整本通讯录——CSV 里尚未建好的单位会自动新建**；
  unit 只能导入自己单位（须先由超管建好，越权报错）；
  **编码自动识别**：UTF-8（含 BOM）/ GB2312 / GBK / GB18030 / UTF-16（含无 BOM），
  中文 Windows 上 Excel/WPS 直接「另存为 CSV」的 GB2312 文件可直接导入（见 `internal/api/csv_encoding.go`）；
  响应带 `encoding` 字段回报识别到的编码，无法识别时返回 400 而不会导入乱码

单位（写仅 super）
- `GET /api/units`（任一管理员）· `POST /api/units` · `PUT /api/units/{id}` · `DELETE /api/units/{id}`

管理员账号（仅 super）
- `GET  /api/admin/users` · `POST /api/admin/users`（role+绑定单位）
- `PUT  /api/admin/users/{id}` · `POST /api/admin/users/{id}/reset-password`（重置后强制改密、旧会话失效）

日志（仅 super）
- `GET /api/logs?category=&user=&from=&to=&page=&pageSize=`（倒序分页，`{total,page,pageSize,items}`）

系统设置（仅 root，管理页「激活码 → 系统设置」）：每日激活码上限、默认账号密码、
激活码/日志保留天数、每日维护时间、自动整理开关，以及
**数据发布间隔 `versionQuietMinutes`**（静默窗口分钟数，默认 15，0 = 有改动即发布）。

> 数据同步机制（脏标记 + 静默窗口合并发布 + gzip）：
> 增删改在**自己的事务里只打脏标记**（`meta.data_dirty=1`）并刷新 `updated_at`，**不**递增版本号；
> 后台发布器等到「静默窗口」到期——即**最后一条改动之后满 `version_quiet_minutes`（默认 15 分钟）
> 没有新改动**——才把这段时间累积的全部改动**合并成一次** `meta.data_version` +1 发布。
> 窗口内又有改动则重新计时；`version_quiet_minutes=0` 表示不延迟（一有改动就发布）。
> 管理页顶栏会提示「有改动待发布（预计 … 自动同步）」，管理员也可点「立即发布」
> （`POST /api/version/publish`）让改动马上同步到手机端。
> 于是连续改 N 条记录（连续编辑、批量导入）只产生 1 个版本、客户端只全量拉取 1 次，
> 不会出现「改一条记录就多一个版本」把服务器压垮；脏标记持久化在库里，进程重启也不会漏掉。
> 客户端仍是：先请求 `GET /api/version`（几十字节），与本地缓存版本**不一致即视为过期（脏）**，
> 才拉 `GET /api/contacts` 全量；一致则直接用缓存。全量响应统一经 gzip 压缩
> （`Content-Encoding: gzip`，按 `Accept-Encoding` 协商）。
> `GET /api/contacts` 返回的版本号取「读联系人之前」的值，因此任何并发写入/发布都只会让客户端
> 多重拉一次，绝不会出现「旧数据配新版本」而永不更新。
> 天然保证最终一致：无论本地多旧、还是服务器换了库（版本回退），不匹配都会触发全量重拉。

## 开发：修改管理页

管理页源码在 `web-admin/`（Vite + Vue3 + Element Plus），构建产物输出到 `web/`
并被 `main.go` 以 `go:embed all:web` 打进二进制。

```bash
cd web-admin
npm install
npm run dev     # 本地开发，热更新；/api 会代理到 http://localhost:8080
npm run build   # 产物写进 ../web，然后再 go build 重新打包 Go 可执行文件
```

## 目录
```
main.go                 入口 + 初始管理员种子 + 数据版本发布器/每日维护 + embed all:web（all: 前缀以包含 _ 开头的 Vite 产物）
internal/config          运行参数
internal/store           SQLite 迁移；contacts/units/users/logs；批量导入；数据脏标记与版本发布（version.go）
internal/auth            bcrypt + 会话令牌生成/校验
internal/api             HTTP 路由、中间件（日志/recover/CORS/gzip）、登录限流、CSV（编码识别/模板/导入导出）、单位/管理员/日志 handler
web/                    内嵌的管理页构建产物（勿手改）
web-admin/              管理页源码（Vite + Vue + Element Plus，侧边栏布局）
```
