package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fnos-mihomo/internal/settings"
	"fnos-mihomo/internal/sub"
)

// fakeCore 是内核控制器的假实现。
type fakeCore struct {
	running bool
	starts  int
	stops   int
}

func (f *fakeCore) IsRunning() bool { return f.running }
func (f *fakeCore) Start() error    { f.starts++; f.running = true; return nil }
func (f *fakeCore) Stop() error     { f.stops++; f.running = false; return nil }
func (f *fakeCore) Restart() error  { return nil }

// newTestService 造一个业务逻辑实例，数据目录用临时目录。
func newTestService(t *testing.T) (*Service, *settings.Store, *fakeCore, string) {
	t.Helper()
	dir := t.TempDir()
	store := settings.NewStore(filepath.Join(dir, "config.json"))
	c := &fakeCore{}
	return NewService(store, c, dir, nil), store, c, dir
}

// 向导写下的 config.env 应该被套用到设置里。
func TestApplyEnvFileAppliesWizardValues(t *testing.T) {
	dir := t.TempDir()
	etc := filepath.Join(dir, "etc")
	if err := os.MkdirAll(etc, 0o755); err != nil {
		t.Fatal(err)
	}
	content := strings.Join([]string{
		"MIXED_PORT=1080",
		"CONTROLLER_PORT=9091",
		"CONTROLLER_SECRET='my secret'",
		"MODE=global",
		"RULES_MODE=strict",
		"ALLOW_LAN=true",
		"SUB_URL=https://example.com/sub",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(etc, "config.env"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	store := settings.NewStore(filepath.Join(dir, "config.json"))
	ApplyEnvFile(store, etc)

	set := store.Get()
	if set.MixedPort != 1080 {
		t.Errorf("代理端口应该是 1080，实际 %d", set.MixedPort)
	}
	if set.ControllerPort != 9091 {
		t.Errorf("控制端口应该是 9091，实际 %d", set.ControllerPort)
	}
	if set.ControllerSecret != "my secret" {
		t.Errorf("密钥应该还原成 my secret，实际 %q", set.ControllerSecret)
	}
	if set.Mode != "global" || set.RulesMode != "strict" {
		t.Errorf("模式没套用: %q %q", set.Mode, set.RulesMode)
	}
	if !set.AllowLan {
		t.Error("允许局域网应该被打开")
	}
	if len(set.Subscriptions) != 1 {
		t.Fatalf("向导里的订阅链接应该被导入，实际 %d 个", len(set.Subscriptions))
	}
	if set.Subscriptions[0].URL != "https://example.com/sub" {
		t.Errorf("订阅链接不对: %q", set.Subscriptions[0].URL)
	}
}

// 已经初始化过就不该再套用向导值 —— 否则用户改过的设置每次重启都被冲回去。
func TestApplyEnvFileSkipsWhenInitialized(t *testing.T) {
	dir := t.TempDir()
	etc := filepath.Join(dir, "etc")
	if err := os.MkdirAll(etc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(etc, "config.env"), []byte("MIXED_PORT=1080\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := settings.NewStore(filepath.Join(dir, "config.json"))
	if err := store.Update(func(c *settings.Settings) { c.MixedPort = 7891 }); err != nil {
		t.Fatal(err)
	}

	ApplyEnvFile(store, etc)
	if got := store.Get().MixedPort; got != 7891 {
		t.Fatalf("已初始化的配置不该被向导覆盖，实际端口 %d", got)
	}
}

func TestParseShellValue(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"plain", "plain"},
		{"'quoted'", "quoted"},
		{"\"double\"", "double"},
		{"''", ""},
		{"  spaced  ", "spaced"},
	}
	for _, c := range cases {
		if got := parseShellValue(c.in); got != c.want {
			t.Errorf("parseShellValue(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

func TestSubscriptionCacheRoundTrip(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	want := &sub.Parsed{
		Clash:   true,
		Proxies: []sub.Proxy{{Name: "节点1", Type: "ss", Server: "1.2.3.4", Port: 443}},
		Rules:   []string{"MATCH,DIRECT"},
	}
	if err := svc.saveCachedProxies("sub-1", want); err != nil {
		t.Fatalf("写缓存失败: %v", err)
	}
	got, err := svc.loadCachedProxies("sub-1")
	if err != nil {
		t.Fatalf("读缓存失败: %v", err)
	}
	if len(got.Proxies) != 1 || got.Proxies[0].Name != "节点1" {
		t.Errorf("缓存内容不对: %+v", got.Proxies)
	}
	if !got.Clash || len(got.Rules) != 1 {
		t.Errorf("策略组标记没存住: %+v", got)
	}
}

// 没有节点时也要写一份最小配置，否则内核因为配置文件不存在而起不来。
func TestApplyConfigWritesMinimalConfigWithoutNodes(t *testing.T) {
	svc, _, _, dir := newTestService(t)
	if err := svc.ApplyConfig(); err != nil {
		t.Fatalf("生成配置失败: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("没写出 config.yaml: %v", err)
	}
	text := string(b)
	if !strings.Contains(text, "MATCH,DIRECT") {
		t.Errorf("最小配置应该带兜底规则，实际内容:\n%s", text)
	}
}

// 多份订阅合并时，策略组与规则取带策略组的那一份（机场自带策略更贴合用户意图）。
func TestApplyConfigPrefersClashGroups(t *testing.T) {
	svc, store, _, dir := newTestService(t)
	_ = store.Update(func(c *settings.Settings) {
		c.Subscriptions = []settings.Subscription{
			{ID: "plain", URL: "https://example.com/1", Enabled: true},
			{ID: "clash", URL: "https://example.com/2", Enabled: true},
		}
	})

	plain := &sub.Parsed{
		Proxies: []sub.Proxy{{Name: "普通节点", Type: "ss", Server: "1.1.1.1", Port: 443}},
	}
	clash := &sub.Parsed{
		Clash:   true,
		Proxies: []sub.Proxy{{Name: "机场节点", Type: "ss", Server: "2.2.2.2", Port: 443}},
		Groups:  []sub.ClashGroup{{Name: "机场选择", Type: "select", Proxies: []string{"机场节点"}}},
		Rules:   []string{"DOMAIN,example.com,DIRECT"},
	}
	if err := svc.saveCachedProxies("plain", plain); err != nil {
		t.Fatal(err)
	}
	if err := svc.saveCachedProxies("clash", clash); err != nil {
		t.Fatal(err)
	}

	if err := svc.ApplyConfig(); err != nil {
		t.Fatalf("生成配置失败: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.Contains(text, "机场选择") {
		t.Errorf("策略组应该来自 Clash 订阅，实际内容:\n%s", text)
	}
	if !strings.Contains(text, "普通节点") || !strings.Contains(text, "机场节点") {
		t.Errorf("两份订阅的节点都应该在里面，实际内容:\n%s", text)
	}
	if !strings.Contains(text, "DOMAIN,example.com,DIRECT") {
		t.Errorf("规则应该来自 Clash 订阅，实际内容:\n%s", text)
	}
}

// 缓存里没有节点的订阅不算「有节点」。
func TestHasNodesFollowsCacheNotNodeCount(t *testing.T) {
	svc, store, _, _ := newTestService(t)
	_ = store.Update(func(c *settings.Settings) {
		c.Subscriptions = []settings.Subscription{
			{ID: "sub-1", URL: "https://example.com/1", Enabled: true, NodeCount: 10},
		}
	})
	if svc.HasNodes() {
		t.Fatal("缓存为空时不该认为有节点")
	}
	if err := svc.saveCachedProxies("sub-1", &sub.Parsed{
		Proxies: []sub.Proxy{{Name: "节点", Type: "ss", Server: "1.1.1.1", Port: 443}},
	}); err != nil {
		t.Fatal(err)
	}
	if !svc.HasNodes() {
		t.Fatal("缓存里有节点时应该认为有节点")
	}
}
