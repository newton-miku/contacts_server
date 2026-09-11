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

- 联系人字段：**姓名、手机号、单位、职务、邮箱**（可选字段 email/jobTitle 允许 null）；
- `units` 预定义单位表（超管维护，改单位名不影响既有联系人）；
- `users.role`：`super` 超级管理员（管单位/管理员/日志，可管全部数据）｜`unit` 单位管理员（可绑 1..N 个单位）；
- `logs`：审计日志；`meta.data_version`：数据版本（脏标记）。

范围规则：匿名客户端与 super 可见全部；unit 只见/只改其绑定单位。**CSV 不设独立权限**，
导入/导出跟随上述同一数据范围（unit 只能导出/导入自己单位，super 全量）。

## 接口

公开（客户端，无需登录）
- `GET /api/version` → `{"version":N,"updatedAt":"..."}`（几十字节）
- `GET /api/contacts` → `{"version":N,"updatedAt":"...","contacts":[{id,displayName,phoneNumber,unitId,unit,email,jobTitle}]}`
  （带有效登录会话时按角色过滤；无会话=匿名全量）

身份
- `POST /api/auth/login` `{username,password}` → `{token,user}` + Set-Cookie（7 天；user 带 role，unit 还带绑定单位）
- `GET  /api/auth/me` / `POST /api/auth/logout` / `POST /api/auth/password` `{oldPassword,newPassword}`

联系人写（super 或 unit，须已过强制改密；跨自己数据范围写返回 403）
- `POST   /api/contacts` / `PUT /api/contacts/{id}` / `DELETE /api/contacts/{id}`
- `GET    /api/contacts/export`：CSV（UTF-8 BOM；表头 姓名,手机号,单位,职务,邮箱；空库=空模板）
- `POST   /api/contacts/import`：multipart `file`；以「单位+手机号+姓名」为关键字，命中覆盖/否则新增；
  整批一个事务，带行号错误；**超级管理员可一次性导入整本通讯录——CSV 里尚未建好的单位会自动新建**；
  unit 只能导入自己单位（须先由超管建好，越权报错）

单位（写仅 super）
- `GET /api/units`（任一管理员）· `POST /api/units` · `PUT /api/units/{id}` · `DELETE /api/units/{id}`

管理员账号（仅 super）
- `GET  /api/admin/users` · `POST /api/admin/users`（role+绑定单位）
- `PUT  /api/admin/users/{id}` · `POST /api/admin/users/{id}/reset-password`（重置后强制改密、旧会话失效）

日志（仅 super）
- `GET /api/logs?category=&user=&from=&to=&page=&pageSize=`（倒序分页，`{total,page,pageSize,items}`）

> 数据同步机制（过期标识/脏标记 + gzip）：
> 每次增删改在同一事务里把 `meta.data_version` +1 并刷新 `updated_at`。
> 客户端先请求 `GET /api/version`（几十字节），与本地缓存版本**不一致即视为过期（脏）**，
> 才拉 `GET /api/contacts` 全量；一致则直接用缓存。全量响应统一经 gzip 压缩
> （`Content-Encoding: gzip`，按 `Accept-Encoding` 协商）。天然保证最终一致：
> 无论本地多旧、还是服务器换了库（版本回退），不匹配都会触发全量重拉。

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
main.go                 入口 + 初始管理员种子 + embed all:web（all: 前缀以包含 _ 开头的 Vite 产物）
internal/config          运行参数
internal/store           SQLite 迁移；contacts/units/users/logs；批量导入；版本 bump
internal/auth            bcrypt + 会话令牌生成/校验
internal/api             HTTP 路由、中间件（日志/recover/CORS/gzip）、登录限流、CSV、单位/管理员/日志 handler
web/                    内嵌的管理页构建产物（勿手改）
web-admin/              管理页源码（Vite + Vue + Element Plus，侧边栏布局）
```
