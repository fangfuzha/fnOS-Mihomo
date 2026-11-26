package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fnos-mihomo/internal/core"
	"fnos-mihomo/internal/dashboard"
	"fnos-mihomo/internal/settings"
)

// errVerify 是接管验证失败的假错误。
var errVerify = errors.New("接管未生效：TUN 设备未创建")

// fakeCore 是内核控制器的假实现。
type fakeCore struct {
	running  bool
	starts   int
	stops    int
	restarts int
	// startErr 让 Start 失败，用来模拟「内核已在运行」这类情况。
	startErr error
}

func (f *fakeCore) IsRunning() bool { return f.running }
func (f *fakeCore) Start() error {
	f.starts++
	if f.startErr != nil {
		return f.startErr
	}
	f.running = true
	return nil
}
func (f *fakeCore) Stop() error            { f.stops++; f.running = false; return nil }
func (f *fakeCore) Restart() error         { f.restarts++; return nil }
func (f *fakeCore) Uptime() time.Duration  { return time.Minute }
func (f *fakeCore) TailLog(n int) []string { return []string{"log line"} }
func (f *fakeCore) Version() string        { return "v1.19.0" }

// fakeGateway 是接管层的假实现。
type fakeGateway struct {
	active    bool
	applyErr  error
	verifyErr error
	applied   int
	removed   int
}

func (f *fakeGateway) Apply(cfg settings.GatewayConfig) error {
	f.applied++
	return f.applyErr
}
func (f *fakeGateway) Remove() error { f.removed++; return nil }
func (f *fakeGateway) Verify() error { return f.verifyErr }
func (f *fakeGateway) Status() bool  { return f.active }

// testEnv 是一次性测试环境。
type testEnv struct {
	server *Server
	core   *fakeCore
	gw     *fakeGateway
	store  *settings.Store
	mux    *http.ServeMux
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	store := settings.NewStore(filepath.Join(dir, "config.json"))
	c := &fakeCore{}
	gw := &fakeGateway{}
	s := NewServer(store, c, gw)

	applied := 0
	s.ApplyConfig = func() error { applied++; return nil }
	s.HasNodes = func() bool { return false }
	s.FetchSubscription = func(url string) (string, error) {
		return "proxies: []\n", nil
	}
	s.DashboardStatus = func() (string, string) { return "ok", "" }

	mux := http.NewServeMux()
	s.Register(mux)
	return &testEnv{server: s, core: c, gw: gw, store: store, mux: mux}
}

// do 发一个请求，返回记录器。
func (e *testEnv) do(method string, path string, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	// 前端的 api() 每次都带 application/json，测试也照做：
	// 写接口会拒绝不是 JSON 的请求，测试必须跟真实客户端一致。
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

func TestStatusReportsCoreAndStore(t *testing.T) {
	env := newTestEnv(t)
	env.core.running = true
	env.gw.active = true

	rec := env.do(http.MethodGet, "/api/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态接口应该返回 200，实际 %d", rec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("状态接口返回的不是 JSON: %v", err)
	}
	if got["running"] != true {
		t.Errorf("running 应该是 true，实际 %v", got["running"])
	}
	if got["gatewayActive"] != true {
		t.Errorf("gatewayActive 应该是 true，实际 %v", got["gatewayActive"])
	}
	if got["mixedPort"] != float64(7890) {
		t.Errorf("mixedPort 应该是默认的 7890，实际 %v", got["mixedPort"])
	}
	if got["version"] != "v1.19.0" {
		t.Errorf("version 应该来自内核，实际 %v", got["version"])
	}
}

func TestSettingsPostUpdatesValues(t *testing.T) {
	env := newTestEnv(t)
	body := `{"mixedPort":1080,"controllerPort":9091,"controllerSecret":"s","allowLan":true,"mode":"global","rulesMode":"strict","logLevel":"info","dashboard":"zashboard"}`
	rec := env.do(http.MethodPost, "/api/settings", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("保存设置应该返回 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	set := env.store.Get()
	if set.MixedPort != 1080 || set.ControllerPort != 9091 {
		t.Errorf("端口没有保存: %d %d", set.MixedPort, set.ControllerPort)
	}
	if set.Mode != "global" || set.RulesMode != "strict" {
		t.Errorf("模式没有保存: %q %q", set.Mode, set.RulesMode)
	}
}

func TestSettingsPostRejectsBadPort(t *testing.T) {
	env := newTestEnv(t)
	rec := env.do(http.MethodPost, "/api/settings", `{"mixedPort":0,"controllerPort":9090}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法端口应该返回 400，实际 %d", rec.Code)
	}
}

// 前端只提交设置项，订阅列表以服务端为准 —— 不能被一次保存设置清空。
func TestSettingsPostKeepsSubscriptions(t *testing.T) {
	env := newTestEnv(t)
	_ = env.store.Update(func(c *settings.Settings) {
		c.Subscriptions = append(c.Subscriptions, settings.Subscription{ID: "sub-1", URL: "https://example.com/s"})
	})

	rec := env.do(http.MethodPost, "/api/settings", `{"mixedPort":7890,"controllerPort":9090,"subscriptions":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("保存设置应该返回 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if len(env.store.Get().Subscriptions) != 1 {
		t.Fatal("保存设置不该清空订阅列表")
	}
}

func TestSubscriptionUpdateAddsAndFetches(t *testing.T) {
	env := newTestEnv(t)
	rec := env.do(http.MethodPost, "/api/subscriptions/update", `{"name":"机场","url":"https://example.com/sub"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("新增订阅应该返回 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	subs := env.store.Get().Subscriptions
	if len(subs) != 1 {
		t.Fatalf("应该新增一个订阅，实际 %d 个", len(subs))
	}
	if subs[0].Name != "机场" {
		t.Errorf("订阅名没保存: %q", subs[0].Name)
	}
	if subs[0].ID == "" {
		t.Error("新订阅应该自动分配 ID")
	}
}

func TestSubscriptionUpdateRejectsEmptyURL(t *testing.T) {
	env := newTestEnv(t)
	rec := env.do(http.MethodPost, "/api/subscriptions/update", `{"name":"空的","url":"  "}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("空链接应该返回 400，实际 %d", rec.Code)
	}
	if len(env.store.Get().Subscriptions) != 0 {
		t.Fatal("空链接不该新增订阅")
	}
}

func TestSubscriptionDeleteRemoves(t *testing.T) {
	env := newTestEnv(t)
	_ = env.store.Update(func(c *settings.Settings) {
		c.Subscriptions = append(c.Subscriptions,
			settings.Subscription{ID: "sub-1", URL: "https://example.com/1"},
			settings.Subscription{ID: "sub-2", URL: "https://example.com/2"},
		)
	})
	rec := env.do(http.MethodPost, "/api/subscriptions/delete", `{"id":"sub-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("删除订阅应该返回 200，实际 %d", rec.Code)
	}
	subs := env.store.Get().Subscriptions
	if len(subs) != 1 || subs[0].ID != "sub-2" {
		t.Fatalf("应该只剩 sub-2，实际 %+v", subs)
	}
}

// 用户手动停过内核之后，自动启停不该再把它拉起来。
func TestManualStopSurvivesAutoSync(t *testing.T) {
	env := newTestEnv(t)
	env.server.HasNodes = func() bool { return true }

	rec := env.do(http.MethodPost, "/api/core/stop", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("停止内核应该返回 200，实际 %d", rec.Code)
	}
	if env.core.stops != 1 {
		t.Fatalf("内核应该被停过一次，实际 %d 次", env.core.stops)
	}

	env.server.SyncCoreState()
	if env.core.starts != 0 {
		t.Fatalf("手动停过之后不该自动启动，实际启动了 %d 次", env.core.starts)
	}
}

func TestAutoStartWhenNodesAvailable(t *testing.T) {
	env := newTestEnv(t)
	env.server.HasNodes = func() bool { return true }

	env.server.SyncCoreState()
	if env.core.starts != 1 {
		t.Fatalf("有可用节点时应该自动启动内核，实际启动 %d 次", env.core.starts)
	}
}

// 内核已经在跑时点「启动」，不该报 500 —— 已经在跑就是达成了目的。
func TestCoreStartAlreadyRunningIsNotAnError(t *testing.T) {
	env := newTestEnv(t)
	env.core.startErr = core.ErrAlreadyRunning

	rec := env.do(http.MethodPost, "/api/core/start", "")
	if rec.Code != http.StatusOK {
		t.Errorf("内核已在运行时应返回 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
}

// 自动启停撞上「内核已在运行」不算失败。
func TestAutoStartAlreadyRunningIsNotAFailure(t *testing.T) {
	env := newTestEnv(t)
	env.server.HasNodes = func() bool { return true }
	env.core.startErr = core.ErrAlreadyRunning

	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	env.server.SyncCoreState()
	if strings.Contains(buf.String(), "自动启动内核失败") {
		t.Errorf("内核已在运行不该记成失败: %s", buf.String())
	}
}

func TestGatewayToggleAppliesAndVerifies(t *testing.T) {
	env := newTestEnv(t)
	rec := env.do(http.MethodPost, "/api/gateway/toggle", `{"enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("开启接管应该返回 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if env.gw.applied != 1 {
		t.Errorf("应该应用一次接管规则，实际 %d 次", env.gw.applied)
	}
	if !env.store.Get().Gateway.Enabled {
		t.Error("接管开关应该已保存")
	}
}

// 接管会改整机路由：验证不过必须自动撤销，不能把 NAS 留在上不了网的状态。
func TestGatewayToggleRevertsWhenVerifyFails(t *testing.T) {
	env := newTestEnv(t)
	env.gw.verifyErr = errVerify

	rec := env.do(http.MethodPost, "/api/gateway/toggle", `{"enabled":true}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("接管验证失败应该返回 500，实际 %d", rec.Code)
	}
	if env.store.Get().Gateway.Enabled {
		t.Error("验证失败后接管开关应该被自动关掉")
	}
	if env.gw.removed != 1 {
		t.Errorf("验证失败应该撤销一次接管规则，实际 %d 次", env.gw.removed)
	}
}

func TestGatewayToggleOffRemoves(t *testing.T) {
	env := newTestEnv(t)
	_ = env.store.Update(func(c *settings.Settings) { c.Gateway.Enabled = true })

	rec := env.do(http.MethodPost, "/api/gateway/toggle", `{"enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("关闭接管应该返回 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if env.gw.removed != 1 {
		t.Errorf("应该撤销一次接管规则，实际 %d 次", env.gw.removed)
	}
	if env.store.Get().Gateway.Enabled {
		t.Error("接管开关应该已被关掉")
	}
}

func TestGatewayToggleRejectsMissingEnabled(t *testing.T) {
	env := newTestEnv(t)
	rec := env.do(http.MethodPost, "/api/gateway/toggle", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺少 enabled 应该返回 400，实际 %d", rec.Code)
	}
}

// 面板地址要用访问者实际用的那个 Host，不能写死 127.0.0.1 ——
// 否则用户在自己电脑上打开面板，浏览器会去连自己电脑的 9090。
func TestDashboardUsesRequestHost(t *testing.T) {
	env := newTestEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	req.Host = "nas.local:8765"
	rec := httptest.NewRecorder()
	env.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("面板信息接口应该返回 200，实际 %d", rec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("返回的不是 JSON: %v", err)
	}
	url, _ := got["url"].(string)
	if !strings.Contains(url, "nas.local:9090") {
		t.Fatalf("面板地址应该用请求里的 Host，实际 %q", url)
	}
}

// 用户用 IPv6 地址访问面板时，生成的地址里主机名必须带方括号。
// 面板自己拼的是 http://2409::982:9090，浏览器解析不了，代理也连不上内核。
func TestDashboardUsesIPv6RequestHost(t *testing.T) {
	env := newTestEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	req.Host = "[2409:8a55:6e9d:2c04::982]:8765"
	rec := httptest.NewRecorder()
	env.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("面板信息接口应该返回 200，实际 %d", rec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("返回的不是 JSON: %v", err)
	}
	url, _ := got["url"].(string)
	want := "http://[2409:8a55:6e9d:2c04::982]:9090/ui/#/setup?http=true" +
		"&hostname=%5B2409%3A8a55%3A6e9d%3A2c04%3A%3A982%5D&port=9090"
	if url != want {
		t.Fatalf("IPv6 面板地址不匹配:\n got=%s\nwant=%s", url, want)
	}
}

// IPv6 快捷入口：接口要给出本机的全球 IPv6，地址里主机名带方括号。
// 机器上没有全球 IPv6 时列表为空，也不该报错。
func TestDashboardIPv6Entries(t *testing.T) {
	env := newTestEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	req.Host = "192.168.2.191:8765"
	rec := httptest.NewRecorder()
	env.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("面板信息接口应该返回 200，实际 %d", rec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("返回的不是 JSON: %v", err)
	}
	list, _ := got["ipv6"].([]any)
	for _, item := range list {
		e, _ := item.(map[string]any)
		addr, _ := e["addr"].(string)
		url, _ := e["url"].(string)
		if !strings.Contains(addr, ":") {
			t.Errorf("IPv6 条目的地址不对: %q", addr)
		}
		if !strings.HasPrefix(url, "http://[") {
			t.Errorf("IPv6 条目的地址应该带方括号: %q", url)
		}
		if !strings.Contains(url, "hostname=%5B") {
			t.Errorf("IPv6 条目的 hostname 参数应该带方括号: %q", url)
		}
	}
}

// IPv6 入口里的地址必须带方括号，hostname 参数也一样。
func TestDashboardURLBracketsIPv6(t *testing.T) {
	d, ok := dashboard.DashboardByID("metacubexd")
	if !ok {
		t.Fatal("找不到 metacubexd 面板")
	}
	got := dashboardURL(d, "2409:8a55:6e93:afb4::982", 9090, "")
	want := "http://[2409:8a55:6e93:afb4::982]:9090/ui/#/setup?http=true" +
		"&hostname=%5B2409%3A8a55%3A6e93%3Aafb4%3A%3A982%5D&port=9090"
	if got != want {
		t.Fatalf("IPv6 面板地址不匹配:\n got=%s\nwant=%s", got, want)
	}
}

// 只有全球单播的 IPv6 才算可用入口：
// 链路本地浏览器要用，回环是本机，唯一本地里混着内核 TUN 的地址。
func TestFilterGlobalIPv6(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"2409:8a55:6e93:afb4::982", true},
		{"2409:8a55:6e93:afb4:d2df:a9c3:ba7c:9f06", true},
		{"2400:ffff:ffff:ffff::1", true},
		{"fdfe:dcba:9876::1", false}, // 内核 TUN 设备上的地址
		{"fc00::1", false},           // 唯一本地
		{"fe80::1", false},           // 链路本地
		{"::1", false},               // 回环
		{"192.168.2.191", false},     // IPv4
	}
	for _, tc := range cases {
		ip := net.ParseIP(tc.ip)
		if ip == nil {
			t.Fatalf("测试用的 IP 解析不了: %s", tc.ip)
		}
		got := filterGlobalIPv6([]net.Addr{&net.IPNet{IP: ip, Mask: net.CIDRMask(64, 128)}})
		has := len(got) == 1
		if has != tc.want {
			t.Errorf("%s 是否算全球 IPv6: got=%v want=%v", tc.ip, has, tc.want)
		}
	}
}
