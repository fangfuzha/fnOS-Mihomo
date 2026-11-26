package deploy

import (
	"os"
	"path/filepath"
	"testing"
)

// TestParseEnvFile 参数文件的三种写法都要认。
func TestParseEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nas.env")
	content := "# 注释\nexport NAS_HOST=1.2.3.4\nNAS_PASS='p@ss'\nNAS_USER=\"u\"\n\n没有等号\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := parseEnvFile(path)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if m["NAS_HOST"] != "1.2.3.4" {
		t.Errorf("export 前缀没处理: %q", m["NAS_HOST"])
	}
	if m["NAS_PASS"] != "p@ss" {
		t.Errorf("单引号没去掉: %q", m["NAS_PASS"])
	}
	if m["NAS_USER"] != "u" {
		t.Errorf("双引号没去掉: %q", m["NAS_USER"])
	}
	if _, ok := m["没有等号"]; ok {
		t.Error("没有等号的行不该进清单")
	}
}

// TestExpandVars 只展开花括号形式。
func TestExpandVars(t *testing.T) {
	m := map[string]string{"A": "x", "B": "${A}/y"}
	if got := expandVars("${A}/z", m); got != "x/z" {
		t.Errorf("展开不对: %q", got)
	}
	if got := expandVars("${B}", m); got != "x/y" {
		t.Errorf("嵌套展开不对: %q", got)
	}
	// 裸 $ 不能动：密码里常有 $
	if got := expandVars("a$b", m); got != "a$b" {
		t.Errorf("裸 $ 被改了: %q", got)
	}
}

// TestResolveEnvPaths REPO_ROOT 相对参数文件所在目录解析，本地路径再相对它解析。
func TestResolveEnvPaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nas.env")
	content := "NAS_HOST=h\nNAS_USER=u\nNAS_PASS=p\nREPO_ROOT=../..\nPANEL_BIN=build/x/panel\nWEB_DIR=web\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	e, err := resolveEnv(path)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	want := filepath.Clean(filepath.Join(dir, "..", "..", "build", "x", "panel"))
	if e.PanelBin != want {
		t.Errorf("面板路径不对:\n got %s\nwant %s", e.PanelBin, want)
	}
	wantWeb := filepath.Clean(filepath.Join(dir, "..", "..", "web"))
	if e.WebDir != wantWeb {
		t.Errorf("前端目录不对: %s", e.WebDir)
	}
}

// TestResolveEnvMissingHost 缺关键参数要报清楚的错。
func TestResolveEnvMissingHost(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nas.env")
	if err := os.WriteFile(path, []byte("NAS_USER=u\nNAS_PASS=p\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveEnv(path); err == nil {
		t.Fatal("缺 NAS_HOST 时应报错")
	}
}

// TestResolveEnvMissingFile 参数文件不存在时要说清怎么办。
func TestResolveEnvMissingFile(t *testing.T) {
	if _, err := resolveEnv(filepath.Join(t.TempDir(), "nope.env")); err == nil {
		t.Fatal("参数文件不存在时应报错")
	}
}
