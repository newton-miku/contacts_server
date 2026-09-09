package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Manager 负责密码哈希与会话令牌的生成/校验。
type Manager struct {
	TokenBytes int
	SessionTTL time.Duration
}

// New 返回默认配置的 Manager。
func New() *Manager {
	return &Manager{TokenBytes: 32, SessionTTL: 7 * 24 * time.Hour}
}

// HashPassword 使用 bcrypt（内部自带随机盐）计算密码哈希。
func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// NoPasswordHash 表示账号「尚未设置密码」（不可用密码登录）。
// 用哨兵值而非空串：便于界面区分「未设置」与「已设置」，也避免空哈希被误匹配。
const NoPasswordHash = "!"

// CheckPassword 校验明文密码与哈希是否匹配。
// 未设置密码（空串/哨兵值）一律不通过；bcrypt 对非法哈希本来也会失败，这里显式拦一层。
func CheckPassword(hash, password string) bool {
	if hash == "" || hash == NoPasswordHash {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// NewToken 生成 URL 安全的随机令牌（十六进制）。
func (m *Manager) NewToken() (string, error) {
	buf := make([]byte, m.TokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// TokenHash 返回令牌的 SHA-256（库中只存哈希，泄露 DB 也无法直接冒用会话）。
func TokenHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// ExpiresAt 返回从现在起 TTL 之后的过期时间。
func (m *Manager) ExpiresAt() string {
	return time.Now().UTC().Add(m.SessionTTL).Format(time.RFC3339)
}

// ErrUnauthorized 统一代表“未登录/会话失效”。
var ErrUnauthorized = errors.New("unauthorized")
