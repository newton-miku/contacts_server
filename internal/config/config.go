package config

import "os"

// Config 汇集服务运行所需参数（地址、DB 路径、初始管理员）。
type Config struct {
	Addr          string
	DBPath        string
	AdminUser     string
	AdminPassword string
}

// NewFromEnv 从启动参数与环境变量读取配置；seedUser/seedPassword 来自命令行参数，
// 允许通过环境变量覆盖。admin 相关值仅在“users 表为空”时用于首次种子。
func NewFromEnv(seedUser, seedPassword string) Config {
	cfg := Config{
		Addr:          ":8080",
		DBPath:        "phonebook.db",
		AdminUser:     envOr("PB_ADMIN_USER", firstNonEmpty(seedUser, "admin")),
		// 不在代码里留默认口令：未提供时留空，由首次播种时随机生成并打印（见 main.seedAdminIfNeeded）。
		AdminPassword: envOr("PB_ADMIN_PASSWORD", seedPassword),
	}
	return cfg
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
