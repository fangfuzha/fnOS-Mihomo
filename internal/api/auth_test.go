package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"fnos-mihomo/internal/settings"
)

// setPassword 给配置里设一个面板口令。
func setPassword(t *testing.T, env *testEnv, pw string) {
	t.Helper()
	h, err := settings.HashPassword(pw)
	if err != nil {
		t.Fatalf("计算口令哈希失败: %v", err)
	}
	if err := env.store.Update(func(c *settings.Settings) {
		c.PanelUser = settings.DefaultUser
		c.PanelPasswordHash = h
	}); err != nil {
		t.Fatalf("保存口令失败: %v", err)
	}
}

// doAuth 发一个请求，可以带上会话 Cookie。
func (e *testEnv) doAuth(method string, path string, body string, token string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

// tryLogin 尝试登录，返回记录器。
func tryLogin(t *testing.T, env *testEnv, user, pw string) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(`{"user":%q,"password":%q}`, user, pw)
	return env.doAuth(http.MethodPost, "/api/login", body, "")
}

// sessionTokenOf 从登录响应里取出会话令牌。
func sessionTokenOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			return c.Value
		}
	}
	t.Fatalf("登录响应里没有会话 Cookie: %s", rec.Body.String())
	return ""
}

// 没设口令时不拦任何人 —— 内网自用不该被登录框挡住。
func TestAuthDisabledWithoutPassword(t *testing.T) {
	env := newTestEnv(t)
	rec := env.do(http.MethodGet, "/api/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("没设口令时状态接口应该放行，实际 %d: %s", rec.Code, rec.Body.String())
	}
}

// 设了口令后，没登录就调接口应该被挡回去。
func TestAuthBlocksAPIWithoutLogin(t *testing.T) {
	env := newTestEnv(t)
	setPassword(t, env, "right-pass")

	for _, path := range []string{"/api/status", "/api/settings", "/api/subscriptions", "/api/dashboard"} {
		rec := env.do(http.MethodGet, path, "")
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s 未登录时应该返回 401，实际 %d", path, rec.Code)
		}
	}
}

// 密码错了就是 401，且不能顺带把会话发出去。
func TestLoginRejectsWrongPassword(t *testing.T) {
	env := newTestEnv(t)
	setPassword(t, env, "right-pass")

	rec := tryLogin(t, env, "admin", "wrong-pass")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("密码错误应该返回 401，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("登录失败不该下发会话 Cookie")
	}
	// 用户名错了也一样。
	rec = tryLogin(t, env, "someone", "right-pass")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("用户名错误应该返回 401，实际 %d", rec.Code)
	}
}

// 登录成功就带上会话，之后的接口都放行。
func TestLoginGrantsSession(t *testing.T) {
	env := newTestEnv(t)
	setPassword(t, env, "right-pass")

	rec := tryLogin(t, env, "admin", "right-pass")
	if rec.Code != http.StatusOK {
		t.Fatalf("登录成功应该返回 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	token := sessionTokenOf(t, rec)
	if token == "" {
		t.Fatal("登录成功却没有会话令牌")
	}

	got := env.doAuth(http.MethodGet, "/api/status", "", token)
	if got.Code != http.StatusOK {
		t.Fatalf("带着会话访问状态接口应该返回 200，实际 %d: %s", got.Code, got.Body.String())
	}
}

// 登出之后旧会话立刻失效。
func TestLogoutInvalidatesSession(t *testing.T) {
	env := newTestEnv(t)
	setPassword(t, env, "right-pass")

	token := sessionTokenOf(t, tryLogin(t, env, "admin", "right-pass"))
	rec := env.doAuth(http.MethodPost, "/api/logout", "", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("登出应该返回 200，实际 %d", rec.Code)
	}
	// 后端要真的作废这一份会话：光把 Cookie 从浏览器里删掉不够，
	// 拿到过 Cookie 的人还能继续用到过期为止。
	var cleared bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("登出应该下发一个立即过期的会话 Cookie")
	}
	if got := env.doAuth(http.MethodGet, "/api/status", "", token); got.Code != http.StatusUnauthorized {
		t.Fatalf("登出后旧会话应该失效，实际 %d", got.Code)
	}
}

// 鉴权状态接口要能告诉前端「要不要登录」。
func TestAuthStateReportsStatus(t *testing.T) {
	env := newTestEnv(t)

	rec := env.do(http.MethodGet, "/api/auth", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("鉴权状态接口应该返回 200，实际 %d", rec.Code)
	}
	// 每次都解到新的 map：往同一个 map 里解 JSON 不会清掉上一次的字段。
	got := map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("鉴权状态接口返回的不是 JSON: %v", err)
	}
	if got["enabled"] != false {
		t.Error("没设口令时 enabled 应该是 false")
	}
	if got["authenticated"] != true {
		t.Error("没设口令时应该是已认证状态")
	}

	setPassword(t, env, "right-pass")
	rec = env.do(http.MethodGet, "/api/auth", "")
	got = map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("鉴权状态接口返回的不是 JSON: %v", err)
	}
	if got["enabled"] != true {
		t.Error("设了口令后 enabled 应该是 true")
	}
	if got["authenticated"] != false {
		t.Error("没登录时 authenticated 应该是 false")
	}
	if _, ok := got["user"]; ok {
		t.Error("没登录时不该把用户名告诉别人")
	}
	// 登录之后才告诉用户名。
	token := sessionTokenOf(t, tryLogin(t, env, "admin", "right-pass"))
	rec = env.doAuth(http.MethodGet, "/api/auth", "", token)
	got = map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("鉴权状态接口返回的不是 JSON: %v", err)
	}
	if got["authenticated"] != true {
		t.Error("带着会话时 authenticated 应该是 true")
	}
	if got["user"] != "admin" {
		t.Errorf("登录后应该返回用户名，实际 %v", got["user"])
	}
}

// 改了口令，旧会话就作废 —— 否则密码白改。
func TestChangingPasswordInvalidatesSession(t *testing.T) {
	env := newTestEnv(t)
	setPassword(t, env, "old-pass")
	token := sessionTokenOf(t, tryLogin(t, env, "admin", "old-pass"))

	if rec := env.doAuth(http.MethodGet, "/api/status", "", token); rec.Code != http.StatusOK {
		t.Fatalf("改口令前旧会话应该有效，实际 %d", rec.Code)
	}

	h, err := settings.HashPassword("new-pass")
	if err != nil {
		t.Fatalf("计算口令哈希失败: %v", err)
	}
	if err := env.store.Update(func(c *settings.Settings) { c.PanelPasswordHash = h }); err != nil {
		t.Fatalf("保存新口令失败: %v", err)
	}

	if rec := env.doAuth(http.MethodGet, "/api/status", "", token); rec.Code != http.StatusUnauthorized {
		t.Fatalf("改口令后旧会话应该失效，实际 %d", rec.Code)
	}
}

// 设置接口不能把口令哈希吐回给前端。
func TestSettingsNeverReturnPasswordHash(t *testing.T) {
	env := newTestEnv(t)
	setPassword(t, env, "right-pass")

	token := sessionTokenOf(t, tryLogin(t, env, "admin", "right-pass"))
	rec := env.doAuth(http.MethodGet, "/api/settings", "", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("读取设置应该返回 200，实际 %d", rec.Code)
	}
	set := env.store.Get()
	if set.PanelPasswordHash == "" {
		t.Fatal("测试前提不成立：口令哈希没设上")
	}
	if strings.Contains(rec.Body.String(), set.PanelPasswordHash) {
		t.Error("设置接口把口令哈希回传了")
	}
	if strings.Contains(rec.Body.String(), "right-pass") {
		t.Error("设置接口回传了口令")
	}
}

// 通过接口设置口令：磁盘上只留哈希，明文不能落盘。
func TestSetPanelPasswordStoresHashOnly(t *testing.T) {
	env := newTestEnv(t)
	body := `{"mixedPort":7890,"controllerPort":9090,"panelUser":"admin","panelPassword":"s3cret-pass"}`
	rec := env.do(http.MethodPost, "/api/settings", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("保存设置应该返回 200，实际 %d: %s", rec.Code, rec.Body.String())
	}

	set := env.store.Get()
	if set.PanelPasswordHash == "" {
		t.Fatal("口令哈希没保存")
	}
	if set.PanelPassword != "" {
		t.Error("设置里不该留着明文口令")
	}
	if !settings.VerifyPassword("s3cret-pass", set.PanelPasswordHash) {
		t.Error("保存的哈希与口令对不上")
	}
	if !set.AuthEnabled() {
		t.Error("设了口令后应该处于已启用鉴权的状态")
	}

	raw, err := os.ReadFile(env.store.Path())
	if err != nil {
		t.Fatalf("读取配置文件失败: %v", err)
	}
	if strings.Contains(string(raw), "s3cret-pass") {
		t.Error("明文口令写进了配置文件")
	}
}

// 清掉口令后回到免登录状态。
func TestClearPanelPasswordDisablesAuth(t *testing.T) {
	env := newTestEnv(t)
	setPassword(t, env, "right-pass")

	token := sessionTokenOf(t, tryLogin(t, env, "admin", "right-pass"))
	body := `{"mixedPort":7890,"controllerPort":9090,"clearPanelPassword":true}`
	rec := env.doAuth(http.MethodPost, "/api/settings", body, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("保存设置应该返回 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if env.store.Get().AuthEnabled() {
		t.Error("清除口令后不该还处于已启用鉴权的状态")
	}
	if rec := env.do(http.MethodGet, "/api/status", ""); rec.Code != http.StatusOK {
		t.Fatalf("清除口令后接口应该放行，实际 %d", rec.Code)
	}
}

// 改设置时没带口令，已有的口令不能被冲掉。
func TestSaveSettingsKeepsExistingPassword(t *testing.T) {
	env := newTestEnv(t)
	setPassword(t, env, "right-pass")
	before := env.store.Get().PanelPasswordHash

	token := sessionTokenOf(t, tryLogin(t, env, "admin", "right-pass"))
	body := `{"mixedPort":7891,"controllerPort":9090,"panelUser":"admin"}`
	rec := env.doAuth(http.MethodPost, "/api/settings", body, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("保存设置应该返回 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if after := env.store.Get().PanelPasswordHash; after != before {
		t.Error("没填新口令时，原来的口令不该被改掉")
	}
	if env.store.Get().MixedPort != 7891 {
		t.Error("其它设置项应该照常保存")
	}
}

// 写接口只收 JSON：挡住最常见的跨站请求伪造。
func TestWriteRequestMustBeJSON(t *testing.T) {
	env := newTestEnv(t)
	req := httptest.NewRequest(http.MethodPost, "/api/core/start", nil)
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	env.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("非 JSON 的写请求应该被拒，实际 %d", rec.Code)
	}
}


// 登出后立刻重新登录，会话必须能用。
//
// 曾经的坑：登出时间记的是纳秒，会话载荷里记的是秒，
// 同一秒内签发的会话被当成「登出前签发的」而作废 ——
// 登录接口返回 200，但它自己签发的令牌服务端不认，
// 前端于是陷入「登录成功 → 请重新登录」的死循环。
func TestLogoutThenImmediateLoginWorks(t *testing.T) {
	env := newTestEnv(t)
	setPassword(t, env, "right-pass")

	first := sessionTokenOf(t, tryLogin(t, env, "admin", "right-pass"))
	if rec := env.doAuth(http.MethodPost, "/api/logout", "", first); rec.Code != http.StatusOK {
		t.Fatalf("登出应该返回 200，实际 %d", rec.Code)
	}

	// 紧接着就重新登录，不留任何间隔。
	second := sessionTokenOf(t, tryLogin(t, env, "admin", "right-pass"))
	if rec := env.doAuth(http.MethodGet, "/api/status", "", second); rec.Code != http.StatusOK {
		t.Fatalf("登出后重新登录的会话应该可用，实际 %d: %s", rec.Code, rec.Body.String())
	}
}

// 登出只作废自己这一份会话，不该把别的设备一起踢下线。
func TestLogoutOnlyRevokesItsOwnSession(t *testing.T) {
	env := newTestEnv(t)
	setPassword(t, env, "right-pass")

	phone := sessionTokenOf(t, tryLogin(t, env, "admin", "right-pass"))
	pc := sessionTokenOf(t, tryLogin(t, env, "admin", "right-pass"))

	if rec := env.doAuth(http.MethodPost, "/api/logout", "", phone); rec.Code != http.StatusOK {
		t.Fatalf("登出应该返回 200，实际 %d", rec.Code)
	}
	if rec := env.doAuth(http.MethodGet, "/api/status", "", phone); rec.Code != http.StatusUnauthorized {
		t.Fatalf("登出的那份会话应该失效，实际 %d", rec.Code)
	}
	if rec := env.doAuth(http.MethodGet, "/api/status", "", pc); rec.Code != http.StatusOK {
		t.Fatalf("另一份会话不该被连带作废，实际 %d: %s", rec.Code, rec.Body.String())
	}
}

// 没登录就不能登出：不然同网段任何人都能把管理员踢下线。
func TestLogoutRequiresSession(t *testing.T) {
	env := newTestEnv(t)
	setPassword(t, env, "right-pass")

	victim := sessionTokenOf(t, tryLogin(t, env, "admin", "right-pass"))

	// 不带任何会话来登出。
	rec := env.doAuth(http.MethodPost, "/api/logout", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("没登录的登出请求应该被拒，实际 %d", rec.Code)
	}
	// 用 GET 也不行：写操作不能走 GET。
	rec = env.doAuth(http.MethodGet, "/api/logout", "", "")
	if rec.Code == http.StatusOK {
		t.Fatalf("GET 方式的登出请求不该被接受，实际 %d", rec.Code)
	}
	// 别人的会话还在。
	if got := env.doAuth(http.MethodGet, "/api/status", "", victim); got.Code != http.StatusOK {
		t.Fatalf("管理员的会话不该被无效的登出请求作废，实际 %d", got.Code)
	}
}

// HEAD 是只读请求，不该按写请求来卡内容类型。
func TestHeadRequestIsNotBlocked(t *testing.T) {
	env := newTestEnv(t)
	req := httptest.NewRequest(http.MethodHead, "/api/status", nil)
	rec := httptest.NewRecorder()
	env.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD 请求不该被拒，实际 %d", rec.Code)
	}
}

// 连续输错密码要锁一下，别让人一直试。
func TestLoginLockoutAfterRepeatedFailures(t *testing.T) {
	env := newTestEnv(t)
	setPassword(t, env, "right-pass")

	for i := 0; i < loginFailMax; i++ {
		rec := tryLogin(t, env, "admin", "wrong-pass")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错误尝试应该返回 401，实际 %d", i+1, rec.Code)
		}
	}
	// 锁定期内即便密码正确也不放行。
	rec := tryLogin(t, env, "admin", "right-pass")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("连续失败过多应该返回 429，实际 %d: %s", rec.Code, rec.Body.String())
	}
}
