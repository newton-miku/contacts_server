package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"phonebook-server/internal/api"
	"phonebook-server/internal/auth"
	"phonebook-server/internal/config"
	"phonebook-server/internal/store"
)

// all:web —— embed 默认会跳过 _/. 开头的文件，Vite 产物里恰好有 _plugin-vue_*.js
//
//go:embed all:web
var webFS embed.FS

func main() {
	// 应急子命令：忘记管理员密码导致无法登录时，在服务器本机控制台执行
	//   phonebook-server.exe recover-admin            # 新密码从环境变量 PB_RECOVER_PASSWORD 读取，
	//   phonebook-server.exe recover-admin -db xxx.db # 或按提示在控制台输入；可带 -user 指定超管
	if len(os.Args) > 1 && os.Args[1] == "recover-admin" {
		runRecoverAdmin(os.Args[2:])
		return
	}

	var (
		addr   = flag.String("addr", ":8080", "HTTP 监听地址")
		dbPath = flag.String("db", "phonebook.db", "SQLite 数据库文件路径")
		seedUs = flag.String("admin-user", "", "首次创建管理员用户名（可被 PB_ADMIN_USER 覆盖）")
		seedPw = flag.String("admin-password", "", "首次创建管理员密码（可被 PB_ADMIN_PASSWORD 覆盖）")
	)
	flag.Parse()

	cfg := config.NewFromEnv(*seedUs, *seedPw)
	cfg.Addr = *addr
	cfg.DBPath = *dbPath

	if err := run(cfg); err != nil {
		log.Fatalf("phonebook-server: %v", err)
	}
}

func run(cfg config.Config) error {
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	// 首次启动且无账号 → 创建初始管理员（bcrypt 加盐后入库）。
	if err := seedAdminIfNeeded(cfg, st); err != nil {
		return err
	}

	static, err := fs.Sub(webFS, "web")
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.NewServer(st, static).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// 每日维护：到点清理失效激活码 + 过期日志；若本次有删除且开启自动整理，则 VACUUM 归还磁盘。
	// 维护时刻由 root 在系统设置里配置（默认 03:30，本地时间）。
	go func() {
		for {
			ctx := context.Background()
			hhmm, err := st.MaintenanceTime(ctx)
			if err != nil {
				hhmm = "03:30"
			}
			d := time.Until(store.NextMaintenanceAt(hhmm, time.Now()))
			if d < time.Minute {
				d = time.Minute
			}
			log.Printf("下次自动维护：%s", time.Now().Add(d).Format("2006-01-02 15:04"))
			time.Sleep(d)
			runMaintenance(st)
		}
	}()

	// 优雅退出
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 数据版本发布器：写操作只打脏标记，这里等「静默窗口」内不再有改动后合并成一次版本 +1，
	// 避免连续改 N 条记录就产生 N 个版本、把手机端的全量拉取放大 N 倍。
	// 窗口由 root 在系统设置里配置（默认 15 分钟，0 = 有改动即发布）；管理员也可手动立即发布。
	go st.RunVersionPublisher(ctx, store.PublisherOptions{
		OnPublish: func(version int64, published bool, err error) {
			switch {
			case err != nil:
				log.Printf("数据版本发布失败（稍后重试）：%v", err)
			case published:
				log.Printf("数据版本已发布：%d（静默窗口内无新改动，改动已合并发布）", version)
			}
		},
	})

	go func() {
		log.Printf("phonebook-server 已启动，监听 %s（管理页：http://localhost%s）", cfg.Addr, cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("listen: %v", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Println("正在关闭…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// runRecoverAdmin 应急重置超管密码（忘记密码导致无法登录时使用）。
// 仅在服务器本机控制台执行、不启动 Web 服务。重置后置 must_change_password=1：
// 下次用新密码登录会被强制改成自己的密码；同时使该账号旧会话全部失效并记一条审计日志。
// 新密码优先从环境变量 PB_RECOVER_PASSWORD 读取（避免明文出现在命令行/进程列表），
// 否则交互式从控制台输入。运行前请先停掉正在运行的 phonebook-server。
func runRecoverAdmin(args []string) {
	fs := flag.NewFlagSet("recover-admin", flag.ExitOnError)
	dbPath := fs.String("db", "phonebook.db", "SQLite 数据库文件路径")
	username := fs.String("user", "admin", "要恢复的超级管理员用户名")
	if err := fs.Parse(args); err != nil {
		log.Fatalf("参数解析失败：%v", err)
	}

	ctx := context.Background()
	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("打开数据库失败：%v", err)
	}
	defer st.Close()

	u, err := st.UserByUsername(ctx, *username)
	if err != nil {
		log.Fatalf("找不到用户 %q——请确认 -db 指向正在使用的数据库、用户名拼写正确（默认 admin）", *username)
	}
	if !store.IsSuperRole(u.Role) {
		log.Fatalf("用户 %q 不是超级管理员（当前角色 %s），应急重置仅对超管账号开放", *username, u.Role)
	}

	pw := os.Getenv("PB_RECOVER_PASSWORD")
	if pw == "" {
		fmt.Print("请输入新的管理员密码：")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			log.Fatalf("读取输入失败：%v", err)
		}
		pw = strings.TrimSpace(line)
	}
	if pw == "" {
		log.Fatal("新密码不能为空")
	}
	if len(pw) < 6 {
		log.Fatal("新密码至少 6 位")
	}

	hash, err := auth.HashPassword(pw)
	if err != nil {
		log.Fatalf("生成密码哈希失败：%v", err)
	}
	if err := st.ResetUserPassword(ctx, u.ID, hash); err != nil {
		log.Fatalf("重置密码失败：%v", err) // must_change_password 置 1
	}
	if err := st.DeleteUserSessions(ctx, u.ID); err != nil {
		log.Fatalf("使旧会话失效失败：%v", err)
	}
	_ = st.AppendLog(ctx, 0, "system", store.LogCategoryAccount, "recover-admin",
		"应急重置管理员密码（用户名 "+u.Username+"），并强制其下次登录改密", "console")

	fmt.Fprintln(os.Stderr, "================================================================")
	fmt.Fprintf(os.Stderr, " 已应急重置超级管理员 %s 的密码。\n", u.Username)
	fmt.Fprintln(os.Stderr, " 该账号已被标记为“下次登录需修改密码”，旧登录会话已全部失效。")
	fmt.Fprintln(os.Stderr, " 现在可以用新密码登录管理页，系统会要求改成你自己的密码。")
	fmt.Fprintln(os.Stderr, "================================================================")
}

func seedAdminIfNeeded(cfg config.Config, st *store.Store) error {
	ctx := context.Background()
	hasUsers, err := st.HasUsers(ctx)
	if err != nil {
		return err
	}
	if hasUsers {
		return nil
	}

	// 不使用任何默认口令：未提供时随机生成一个并在控制台打印，避免「默认密码」被长期沿用。
	password := cfg.AdminPassword
	generated := false
	if strings.TrimSpace(password) == "" {
		pw, err := randomPassword(16)
		if err != nil {
			return fmt.Errorf("generate seed password: %w", err)
		}
		password = pw
		generated = true
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash seed password: %w", err)
	}
	// 种子账号必须是最高管理员（root），且首次登录改密，避免默认密码长期可用。
	if _, err := st.CreateUser(ctx, cfg.AdminUser, hash, "管理员", store.RoleRoot, true); err != nil {
		return fmt.Errorf("seed admin: %w", err)
	}

	fmt.Fprintln(os.Stderr, "================================================================")
	fmt.Fprintf(os.Stderr, " 已创建初始管理员账号\n")
	fmt.Fprintf(os.Stderr, "   用户名：%s\n", cfg.AdminUser)
	fmt.Fprintf(os.Stderr, "   密码：  %s\n", password)
	if generated {
		fmt.Fprintln(os.Stderr, "   （本次为随机生成，请立即妥善保存）")
	}
	fmt.Fprintln(os.Stderr, " 首次登录管理页时会被要求修改该密码。")
	fmt.Fprintln(os.Stderr, "================================================================")
	return nil
}

// runMaintenance 执行一次维护：清理失效激活码与过期日志，必要时整理数据库。
func runMaintenance(st *store.Store) {
	ctx := context.Background()
	var deleted int64
	if days, err := st.ActivationCodeRetentionDays(ctx); err == nil && days > 0 {
		if n, err := st.CleanupActivationCodes(ctx, days); err == nil && n > 0 {
			log.Printf("已清理失效激活码 %d 条（保留 %d 天）", n, days)
			deleted += n
		}
	}
	if days, err := st.LogRetentionDays(ctx); err == nil && days > 0 {
		if n, err := st.CleanupLogs(ctx, days); err == nil && n > 0 {
			log.Printf("已清理审计日志 %d 条（保留 %d 天）", n, days)
			deleted += n
		}
	}
	if deleted == 0 {
		return
	}
	on, err := st.AutoVacuumEnabled(ctx)
	if err != nil || !on {
		return
	}
	before := st.DBFileSize()
	if err := st.Vacuum(ctx); err != nil {
		log.Printf("数据库整理失败：%v", err)
		return
	}
	log.Printf("数据库整理完成：%d → %d 字节（本次清理 %d 条）", before, st.DBFileSize(), deleted)
}

// randomPassword 生成 n 位随机口令（大小写字母+数字），用于首次播种管理员账号。
func randomPassword(n int) (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, b := range buf {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out), nil
}
