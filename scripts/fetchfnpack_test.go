package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// 下载下来的文件默认没有可执行位，Linux 上 fork/exec 会 permission denied。
func TestMakeExecutable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "fnpack")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := makeExecutable(p); err != nil {
		t.Fatalf("加可执行位失败: %v", err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("还是不可执行: %v", info.Mode())
	}
}
