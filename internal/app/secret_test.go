package app

import (
	"path/filepath"
	"testing"

	"fnos-mihomo/internal/settings"
)

// 首次运行要自动生成控制密钥：控制口绑在所有网卡上，空密钥等于裸奔。
func TestEnsureControllerSecretGeneratesKey(t *testing.T) {
	dir := t.TempDir()
	store := settings.NewStore(filepath.Join(dir, "config.json"))

	EnsureControllerSecret(store)
	secret := store.Get().ControllerSecret
	if secret == "" {
		t.Fatal("首次运行没有生成控制密钥")
	}
	if len(secret) != 32 {
		t.Errorf("控制密钥长度不对: %d", len(secret))
	}

	// 已经有密钥时不该覆盖：用户自己设的要保留。
	EnsureControllerSecret(store)
	if store.Get().ControllerSecret != secret {
		t.Error("已有的控制密钥被覆盖了")
	}
}

// 用户显式清空密钥后，重启不该又给生成一个。
func TestExplicitlyClearedSecretIsNotRegenerated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	// 第一次运行：生成密钥。
	store := settings.NewStore(path)
	if !store.IsFresh() {
		t.Fatal("新建的配置不算是首次运行")
	}
	EnsureControllerSecret(store)
	secret := store.Get().ControllerSecret
	if secret == "" {
		t.Fatal("首次运行没有生成控制密钥")
	}

	// 用户显式清空。
	if err := store.Update(func(c *settings.Settings) { c.ControllerSecret = "" }); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	// 重启：配置已存在，不是首次运行，不该再生成。
	store2 := settings.NewStore(path)
	if store2.IsFresh() {
		t.Fatal("已存在的配置不该算首次运行")
	}
	EnsureControllerSecret(store2)
	if store2.Get().ControllerSecret != "" {
		t.Error("显式清空的密钥不该被重新生成")
	}
}
