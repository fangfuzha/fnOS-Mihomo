package settings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestIPv6DefaultEnabled 默认开启 IPv6 代理。
func TestIPv6DefaultEnabled(t *testing.T) {
	if !Default().IPv6Enabled() {
		t.Error("默认应开启 IPv6 代理")
	}
}

// TestIPv6OldConfigStaysEnabled 老版本的 config.json 里没有 ipv6 字段，
// 升级后应该仍然是开启 —— mihomo 自身的默认值也是 true，
// 不能因为加了个设置项就让老用户莫名其妙少了一半协议。
func TestIPv6OldConfigStaysEnabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"mixedPort":7890,"mode":"rule"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(path)
	if !s.Get().IPv6Enabled() {
		t.Error("老配置升级后应保持开启")
	}
}

// TestConcurrentUpdate 并发改配置：每条改动都得落盘，
// 落盘文件也必须是完整的一份 JSON。
//
// Save 之前用读锁，两个 Save 并发时旧的状态可能后改名盖掉新的状态。
func TestConcurrentUpdate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	s := NewStore(path)

	const n = 16
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errCh <- s.Update(func(c *Settings) {
				c.Subscriptions = append(c.Subscriptions, Subscription{
					ID:      fmt.Sprintf("sub-%d", i),
					Name:    "n",
					Enabled: true,
				})
			})
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("并发更新报错: %v", err)
		}
	}

	// 每条订阅都得在：并发写不能把别人的改动吞掉。
	seen := map[string]bool{}
	for _, sb := range s.Get().Subscriptions {
		seen[sb.ID] = true
	}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("sub-%d", i)
		if !seen[id] {
			t.Errorf("订阅 %s 丢了", id)
		}
	}

	// 落盘的文件是完整 JSON。
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk Settings
	if err := json.Unmarshal(b, &onDisk); err != nil {
		t.Fatalf("落盘的不是完整 JSON: %v", err)
	}
	if len(onDisk.Subscriptions) != n {
		t.Errorf("落盘的订阅数不对: %d，期望 %d", len(onDisk.Subscriptions), n)
	}

	// 不能留下临时文件。
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "config.json" {
			t.Errorf("残留了临时文件: %s", e.Name())
		}
	}
}

// TestIPv6ExplicitlyDisabled 用户明确写 false 时要能读回来。
func TestIPv6ExplicitlyDisabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"mixedPort":7890,"ipv6":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(path)
	if s.Get().IPv6Enabled() {
		t.Error("明确写 false 时应读回关闭")
	}
}
