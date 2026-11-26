package deploy

import (
	"os"
	"path/filepath"
	"testing"
)

// TestIsProjectRoot 根目录认得准，不能把 scripts/deploy 自己也当成根。
func TestIsProjectRoot(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if !isProjectRoot(root) {
		t.Errorf("%s 不该被认成项目根", root)
	}
	if isProjectRoot(".") {
		t.Error("deploy 自己的目录不该被认成项目根")
	}
}

// TestFindEnvFile 从子目录往上找，能找到项目根的 .env。
func TestFindEnvFile(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	got := findEnvFile(root)
	want := filepath.Join(root, ".env")
	if got != want {
		t.Errorf("找到的路径不对:\n got %s\nwant %s", got, want)
	}

	// 从根目录本身找，结果也该是同一个。
	if got := findEnvFile(root); got != want {
		t.Errorf("从根目录找不对: %s", got)
	}
}

// TestResolveEnvUsesProjectRoot 参数文件在项目根时，本地路径按项目根解析。
func TestResolveEnvUsesProjectRoot(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".env")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("项目根没有 .env，跳过: %v", err)
	}
	e, err := resolveEnv(path)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if e.RepoRoot != root {
		t.Errorf("REPO_ROOT 不对:\n got %s\nwant %s", e.RepoRoot, root)
	}
	want := filepath.Join(root, "build", "amd64", "app", "bin", "panel")
	if e.PanelBin != want {
		t.Errorf("面板路径不对:\n got %s\nwant %s", e.PanelBin, want)
	}
}
