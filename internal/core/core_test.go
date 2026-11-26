package core

import (
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// TestMain 拦下被当成「内核」拉起来的那份测试进程。
//
// TestMain 在 flag 解析之前跑，所以子进程收到内核那套看不懂的参数也不会报错退出。
func TestMain(m *testing.M) {
	if os.Getenv("MIHOMO_FAKE_CORE") == "1" {
		runFakeCore()
		return
	}
	os.Exit(m.Run())
}

// runFakeCore 假装 mihomo：写「活着」标记，退出时写「已退出」标记。
func runFakeCore() {
	dir := os.Getenv("MIHOMO_FAKE_CORE_DIR")
	_ = os.WriteFile(filepath.Join(dir, "alive"), []byte(strconv.Itoa(os.Getpid())), 0o600)

	// Linux 上内核是被 SIGINT 停掉的：收到信号就写标记退出。
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		<-sig
		_ = os.WriteFile(filepath.Join(dir, "exited"), []byte("1"), 0o600)
		os.Exit(0)
	}()

	// Windows 上没法给别的进程发 SIGINT，等它自己到点退出。
	time.Sleep(time.Second)
	_ = os.WriteFile(filepath.Join(dir, "exited"), []byte("1"), 0o600)
	os.Exit(0)
}

// newTestCore 造一个用测试进程冒充内核的 Core。
func newTestCore(t *testing.T) (*Core, string) {
	t.Helper()
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("mixed-port: 7890\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIHOMO_FAKE_CORE", "1")
	t.Setenv("MIHOMO_FAKE_CORE_DIR", work)
	return New(os.Args[0], cfg, work), work
}

// waitFile 等一个文件出现。
func waitFile(t *testing.T, path string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等不到文件: %s", path)
}

// TestRestartConcurrent 并发重启不该报错。
//
// Restart 是「停 + 起」两步：两步各自加锁时两个 goroutine 会交错，
// 一个报「内核已在运行」把请求打成 500，
// 更糟的是一个的 stop 会把另一个刚拉起来的内核又停掉。
func TestRestartConcurrent(t *testing.T) {
	c, _ := newTestCore(t)
	if err := c.Start(); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	t.Cleanup(func() { _ = c.Stop() })

	const n = 4
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- c.Restart()
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Errorf("并发重启不该报错: %v", err)
		}
	}
}

// TestStopWaitsForProcessExit Stop 返回时进程必须真的退出了。
//
// 不等就返回的话，新内核起来时老内核还占着端口。
func TestStopWaitsForProcessExit(t *testing.T) {
	c, work := newTestCore(t)
	if err := c.Start(); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	waitFile(t, filepath.Join(work, "alive"))

	if err := c.Stop(); err != nil {
		t.Fatalf("停止失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, "exited")); err != nil {
		t.Errorf("Stop 返回了但内核进程还没退出: %v", err)
	}
	if c.IsRunning() {
		t.Error("停止后不该还显示在运行")
	}
}

// TestStartTwiceReportsAlreadyRunning 已经在跑时再启动，报的是那个专门的错。
func TestStartTwiceReportsAlreadyRunning(t *testing.T) {
	c, _ := newTestCore(t)
	if err := c.Start(); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	t.Cleanup(func() { _ = c.Stop() })

	if err := c.Start(); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("第二次启动应报「内核已在运行」，实际 %v", err)
	}
}
