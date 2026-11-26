package mihomo

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

	"fnos-mihomo/internal/settings"
	"fnos-mihomo/internal/sub"
)

func TestSecretEmptyNotWritten(t *testing.T) {
	set := settings.Default()
	set.ControllerSecret = ""
	parsed := &sub.Parsed{
		Proxies: []sub.Proxy{{Name: "n1", Type: "vless", Server: "a.com", Port: 443, UUID: "u"}},
	}

	cfg, err := Build(*set, parsed)
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}
	if cfg.Secret != nil {
		t.Errorf("空密钥应输出 nil，实际 %q", *cfg.Secret)
	}

	b, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	out := string(b)
	if strings.Contains(out, "secret:") {
		t.Errorf("空密钥不应写出 secret 字段:\n%s", firstLines(out, 15))
	}
}

func TestSecretNonEmptyWritten(t *testing.T) {
	set := settings.Default()
	set.ControllerSecret = "mysecret"
	parsed := &sub.Parsed{
		Proxies: []sub.Proxy{{Name: "n1", Type: "vless", Server: "a.com", Port: 443, UUID: "u"}},
	}

	cfg, _ := Build(*set, parsed)
	if cfg.Secret == nil || *cfg.Secret != "mysecret" {
		t.Fatalf("密钥应保留")
	}
}

func TestDedupeAcrossSubscriptions(t *testing.T) {
	set := settings.Default()
	parsed := &sub.Parsed{
		Proxies: []sub.Proxy{
			{Name: "dup", Type: "vless", Server: "a.com", Port: 443, UUID: "u1"},
			{Name: "dup", Type: "vless", Server: "b.com", Port: 443, UUID: "u2"},
			{Name: "other", Type: "vless", Server: "c.com", Port: 443, UUID: "u3"},
		},
		Groups: []sub.ClashGroup{
			{Name: "G", Type: "select", Proxies: []string{"dup", "other"}},
		},
		Clash: true,
	}

	cfg, err := Build(*set, parsed)
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}
	seen := map[string]bool{}
	for _, p := range cfg.Proxies {
		if seen[p.Name] {
			t.Errorf("仍有重名节点: %s", p.Name)
		}
		seen[p.Name] = true
	}
	if len(cfg.Proxies) != 3 {
		t.Errorf("期望 3 个节点，实际 %d", len(cfg.Proxies))
	}
	// 策略组里的引用也要指向存在的节点
	for _, g := range cfg.ProxyGroups {
		for _, ref := range g.Proxies {
			if !seen[ref] {
				t.Errorf("策略组 %s 引用了不存在的节点 %s", g.Name, ref)
			}
		}
	}
}

// TestWriteConfigConcurrent 并发写配置。
//
// 启动时后台刷新订阅与自动启停会同时写 config.yaml：
// 两处共用固定的 config.yaml.tmp 时，一个先改名走，
// 另一个再改名就报「no such file or directory」（NAS 日志里那条就是它）。
//
// 临时文件名唯一之后，并发写不该报错，落地也必须是完整的一份。
func TestWriteConfigConcurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	const n = 8
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			set := settings.Default()
			cfg, err := Build(*set, &sub.Parsed{
				Proxies: []sub.Proxy{{Name: fmt.Sprintf("n%d", i), Type: "vless", Server: "a.com", Port: 443, UUID: "u"}},
			})
			if err != nil {
				errCh <- err
				return
			}
			errCh <- WriteConfig(path, cfg)
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("并发写配置报错: %v", err)
		}
	}

	// 落地的是某一份完整的配置，不是半截 YAML。
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	var got map[string]any
	if err := yaml.Unmarshal(b, &got); err != nil {
		t.Fatalf("配置不是完整 YAML: %v\n%s", err, string(b))
	}
	proxies, ok := got["proxies"].([]any)
	if !ok || len(proxies) != 1 {
		t.Errorf("配置里的节点数不对: %v", got["proxies"])
	}

	// 不能留下临时文件。
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "config.yaml" {
			t.Errorf("残留了临时文件: %s", e.Name())
		}
	}
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
