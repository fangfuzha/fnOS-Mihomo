package settings

// 面板口令的哈希与校验。
//
// 口令不存明文：配置里只留 PBKDF2 派生出来的哈希。
// 就算 config.json 被人拿走，也猜不出原始口令。

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

const (
	// pbkdf2Iterations 是派生迭代次数。
	// 21 万次 SHA-256 在普通 CPU 上是 100 毫秒量级：
	// 登录一次无感，暴力猜口令则慢到不可行。
	pbkdf2Iterations = 210000

	// pbkdf2SaltLen 是盐的长度（RFC 8018 建议至少 8 字节）。
	pbkdf2SaltLen = 16

	// pbkdf2KeyLen 是派生密钥长度。
	pbkdf2KeyLen = 32
)

// hashPrefix 标记哈希的算法与格式，格式变了靠它区分。
const hashPrefix = "pbkdf2-sha256"

// HashPassword 计算面板口令的哈希，返回可直接存进配置的字符串。
//
// 格式：pbkdf2-sha256$迭代次数$盐(base64)$哈希(base64)
// 每个口令配独立的随机盐，所以同一个口令两次算出来的结果不同。
func HashPassword(password string) (string, error) {
	salt := make([]byte, pbkdf2SaltLen)
	// Go 1.24 起 crypto/rand.Read 失败会 panic，不会返回错误。
	_, _ = rand.Read(salt)
	dk, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, pbkdf2KeyLen)
	if err != nil {
		return "", fmt.Errorf("派生口令哈希失败: %w", err)
	}
	return strings.Join([]string{
		hashPrefix,
		strconv.Itoa(pbkdf2Iterations),
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(dk),
	}, "$"), nil
}

// VerifyPassword 校验口令与哈希是否匹配。
//
// 哈希格式不认识（老配置或文件被改坏）一律当作不匹配。
func VerifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != hashPrefix {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter <= 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(want) == 0 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	if err != nil {
		return false
	}
	// 定长比较，避免用返回时间猜哈希。
	return subtle.ConstantTimeCompare(got, want) == 1
}
