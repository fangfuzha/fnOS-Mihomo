package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 哈希不能等于明文，也不能把口令本身带在里面。
func TestHashPasswordDoesNotLeakPlaintext(t *testing.T) {
	h, err := HashPassword("my-pass")
	if err != nil {
		t.Fatalf("计算哈希失败: %v", err)
	}
	if h == "my-pass" || strings.Contains(h, "my-pass") {
		t.Fatal("哈希里不该出现口令本身")
	}
	if !strings.HasPrefix(h, "pbkdf2-sha256$") {
		t.Fatalf("哈希格式不对: %s", h)
	}
}

// 同一个口令两次哈希结果不同（盐是随机的），但都能校验通过。
func TestHashPasswordUsesRandomSalt(t *testing.T) {
	a, err := HashPassword("same-pass")
	if err != nil {
		t.Fatalf("计算哈希失败: %v", err)
	}
	b, err := HashPassword("same-pass")
	if err != nil {
		t.Fatalf("计算哈希失败: %v", err)
	}
	if a == b {
		t.Error("两次哈希不该相同，说明盐没随机")
	}
	if !VerifyPassword("same-pass", a) || !VerifyPassword("same-pass", b) {
		t.Error("正确的口令应该都能校验通过")
	}
}

// 错误的口令、坏掉的哈希都要判为不通过。
func TestVerifyPasswordRejectsWrongInput(t *testing.T) {
	h, err := HashPassword("right-pass")
	if err != nil {
		t.Fatalf("计算哈希失败: %v", err)
	}
	if VerifyPassword("wrong-pass", h) {
		t.Error("错误的口令不该通过")
	}
	if VerifyPassword("", h) {
		t.Error("空口令不该通过")
	}
	for _, bad := range []string{"", "xxx", "pbkdf2-sha256$1$2$3", "pbkdf2-md5$1$2$3$4", "pbkdf2-sha256$abc$aa$bb"} {
		if VerifyPassword("right-pass", bad) {
			t.Errorf("坏掉的哈希 %q 不该通过", bad)
		}
	}
}

// 旧版本把口令明文写进配置，读的时候要迁成哈希并抹掉明文。
func TestLegacyPlaintextPasswordIsMigrated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	raw := `{"mixedPort":7890,"panelUser":"admin","panelPassword":"legacy-plain"}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}

	store := NewStore(path)
	set := store.Get()
	if set.PanelPassword != "" {
		t.Error("明文口令没有清掉")
	}
	if !set.AuthEnabled() {
		t.Error("迁移后应该处于已启用鉴权的状态")
	}
	if !VerifyPassword("legacy-plain", set.PanelPasswordHash) {
		t.Error("迁移后的哈希校验不过")
	}

	// 迁移要落盘：明文不能继续躺在文件里。
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	if strings.Contains(string(saved), "legacy-plain") {
		t.Error("明文口令还留在配置文件里")
	}
}

// 用户名留空时按 admin 处理。
func TestEffectiveUserFallsBackToAdmin(t *testing.T) {
	s := Settings{}
	if s.EffectiveUser() != DefaultUser {
		t.Errorf("没设用户名时应该用 %s，实际 %s", DefaultUser, s.EffectiveUser())
	}
	s.PanelUser = "someone"
	if s.EffectiveUser() != "someone" {
		t.Errorf("应该用设置里的用户名，实际 %s", s.EffectiveUser())
	}
}
