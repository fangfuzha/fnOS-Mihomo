package safeio

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// TestWriteFileContentAndPerm 内容与权限都要对。
func TestWriteFileContentAndPerm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.json")
	if err := WriteFile(path, []byte("内容"), 0o600); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不出来: %v", err)
	}
	if string(b) != "内容" {
		t.Errorf("内容不对: %q", b)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		// Windows 的 os.Chmod 只管「只读」那一个位：
		// 0600 实际是 0666，0400 实际是 0444。
		if info.Mode().Perm()&0o200 == 0 {
			t.Errorf("权限不对: %v（应该可写）", info.Mode().Perm())
		}
	} else if info.Mode().Perm() != 0o600 {
		t.Errorf("权限不对: %v", info.Mode().Perm())
	}
}

// TestWriteFileConcurrent 并发写同一个文件不能出错，也不能写出半份内容。
//
// 用固定 .tmp 的时候：一个 goroutine 先改名走，另一个再改名就报
// 「no such file or directory」；更糟的是两边同时写同一个 .tmp，
// 落地的是半份文件。
func TestWriteFileConcurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.json")
	payloads := make([][]byte, 8)
	for i := range payloads {
		payloads[i] = []byte(strings.Repeat(string(rune('a'+i)), 4096))
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(payloads))
	for i := range payloads {
		wg.Add(1)
		go func(p []byte) {
			defer wg.Done()
			if err := WriteFile(path, p, 0o600); err != nil {
				errs <- err
			}
		}(payloads[i])
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("并发写不该出错: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// 内容必须是某一份完整的，不能是两份拼起来的。
	ok := false
	for _, p := range payloads {
		if string(b) == string(p) {
			ok = true
			break
		}
	}
	if !ok {
		t.Errorf("落地内容不是任何一份完整的: 长度 %d", len(b))
	}

	// 临时文件不能留下。
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(path) {
			t.Errorf("留下了残留文件: %s", e.Name())
		}
	}
}
