package api

// 面板鉴权。
//
// 面板监听在所有网卡上，同网段的任何设备都能访问 8765 端口。
// 设置了面板口令后，除登录接口外的所有接口都要求先登录；
// 没设置口令时保持免登录 —— 内网自用不该被一道登录框挡住。
//
// 登录态用会话 Cookie 承载，不用 HTTP Basic Auth：
// 飞牛桌面是把面板嵌在 iframe 里打开的，
// 浏览器在 iframe 里弹 Basic Auth 认证框会被拦掉，页面只会得到一个空白框。

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"fnos-mihomo/internal/settings"
)

const (
	// sessionCookie 是会话 Cookie 的名字。
	sessionCookie = "mihomo_session"

	// sessionTTL 是会话有效期。
	// 面板重启（热更新、升级应用）后进程内的签名密钥会重新生成，
	// 旧会话自然失效，需要重新登录一次。
	sessionTTL = 24 * time.Hour

	// loginFailMax 是同一来源连续失败多少次后暂时拒绝登录。
	loginFailMax = 5

	// loginLockFor 是连续失败过多后的锁定时长。
	loginLockFor = time.Minute

	// maxFailRecords 是登录失败计数表的上限。
	// 内网设备多、又有人扫端口时，表不至于无限增长。
	maxFailRecords = 256
)

// authGuard 管会话签发与登录失败计数。
type authGuard struct {
	store *settings.Store

	// key 是会话签名的密钥，进程启动时随机生成。
	// 放在内存里而不是配置文件里：重启即失效，不用考虑怎么擦干净。
	key []byte

	mu       sync.Mutex
	failures map[string]*loginFail

	// revoked 记已登出的会话令牌，到期后自动清理。
	//
	// 只作废登出的那一份会话：别让「退出登录」把别的设备也一起踢下线。
	// 整份作废是另一回事，而且没鉴权的接口绝不能碰这个表 ——
	// 否则同网段任何人都能一句话让管理员掉线。
	revoked map[string]time.Time
}

// loginFail 记某个来源的连续登录失败。
type loginFail struct {
	count int
	until time.Time
}

// newAuthGuard 建一个会话守卫。
func newAuthGuard(store *settings.Store) *authGuard {
	key := make([]byte, 32)
	// Go 1.24 起 crypto/rand.Read 失败会 panic，不会返回错误。
	_, _ = rand.Read(key)
	return &authGuard{
		store:    store,
		key:      key,
		failures: map[string]*loginFail{},
		revoked:  map[string]time.Time{},
	}
}

// handleAuthState 报告面板是否启用了登录口令，以及当前请求是不是已登录状态。
// 前端靠它决定是直接进面板，还是先弹登录框。
func (s *Server) handleAuthState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, fmt.Errorf("不支持的方法"))
		return
	}
	set := s.store.Get()
	enabled := set.AuthEnabled()
	authed := !enabled || s.auth.verifyToken(sessionToken(r))
	out := map[string]any{"enabled": enabled, "authenticated": authed}
	if authed {
		out["user"] = set.EffectiveUser()
	}
	writeJSON(w, http.StatusOK, out)
}

// handleLogin 校验用户名与口令，通过后下发会话 Cookie。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, fmt.Errorf("不支持的方法"))
		return
	}
	if !isJSONRequest(r) {
		writeErr(w, http.StatusUnsupportedMediaType, fmt.Errorf("只接受 JSON 请求"))
		return
	}
	ip := clientIP(r)
	if s.auth.locked(ip) {
		writeErr(w, http.StatusTooManyRequests, fmt.Errorf("登录失败次数过多，请一分钟后再试"))
		return
	}
	var in struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	set := s.store.Get()
	if !set.AuthEnabled() {
		// 没设口令时登录没有意义，直接放行。
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if !settings.VerifyPassword(in.Password, set.PanelPasswordHash) || in.User != set.EffectiveUser() {
		s.auth.fail(ip)
		writeErr(w, http.StatusUnauthorized, fmt.Errorf("用户名或密码不正确"))
		return
	}
	s.auth.clearFail(ip)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    s.auth.issue(set),
		Path:     "/",
		HttpOnly: true,
		// Lax 而不是 Strict：面板可能被 iframe 嵌着打开，
		// Strict 下从外部链接跳进来时不会带上 Cookie。
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "user": set.EffectiveUser()})
}

// handleLogout 退出登录：清掉会话 Cookie，并作废后端这一份会话。
//
// 这个接口必须带有效会话（外面套了 s.authed）：
// 无鉴权的话，同网段任何设备发一个请求就能把管理员踢下线。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.auth.revoke(sessionToken(r))
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// authed 给需要登录的接口套上鉴权。
//
// 没设口令时直接放行；设了口令就必须带着有效会话。
// 写操作额外要求 application/json：
// HTML 表单只能发 urlencoded，跨站页面的 fetch 也发不出 application/json，
// 这样就挡住了最常见的一类跨站请求伪造。
func (s *Server) authed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.store.Get().AuthEnabled() && !s.auth.verifyToken(sessionToken(r)) {
			writeErr(w, http.StatusUnauthorized, fmt.Errorf("需要登录"))
			return
		}
		// HEAD 与 GET 一样不改数据，不该按写请求来卡。
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !isJSONRequest(r) {
			writeErr(w, http.StatusUnsupportedMediaType, fmt.Errorf("只接受 JSON 请求"))
			return
		}
		h(w, r)
	}
}

// issue 生成一个会话令牌。
//
// 令牌里带三样东西：用户名、过期时间、口令指纹。
// 带上口令指纹是为了让「改密码」这件事立刻作废旧会话 ——
// 否则改了密码，别人手里拿着的旧 Cookie 还能继续用。
func (a *authGuard) issue(set settings.Settings) string {
	now := time.Now()
	// 载荷里必须带随机 nonce：只靠秒级时间戳的话，
	// 同一秒里的两次登录会得到完全相同的令牌 ——
	// 登出一个就等于把另一个也作废了。
	nonce := make([]byte, 8)
	_, _ = rand.Read(nonce)
	payload := fmt.Sprintf("%s|%d|%d|%s|%s",
		set.EffectiveUser(),
		now.UnixNano(),
		now.Add(sessionTTL).Unix(),
		pwFingerprint(set.PanelPasswordHash),
		base64.RawURLEncoding.EncodeToString(nonce))
	return signToken(a.key, payload)
}

// revoke 作废一个会话令牌（登出时调用）。
//
// 只作废这一个 —— 「退出登录」不该把同一台 NAS 上别的设备也踢下线。
func (a *authGuard) revoke(token string) {
	_, _, exp, _, ok := parseToken(token)
	if !ok {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.revoked[token] = time.Unix(exp, 0)
	// 顺手清掉已经过期的，别让这张表越积越多。
	now := time.Now()
	for k, t := range a.revoked {
		if now.After(t) {
			delete(a.revoked, k)
		}
	}
}

// verifyToken 校验会话令牌是否有效。
func (a *authGuard) verifyToken(token string) bool {
	if token == "" {
		return false
	}
	user, _, exp, fingerprint, ok := parseToken(token)
	if !ok {
		return false
	}
	if time.Now().Unix() > exp {
		return false
	}
	if !a.signatureOK(token) {
		return false
	}
	if a.isRevoked(token) {
		return false
	}
	set := a.store.Get()
	if user != set.EffectiveUser() {
		return false
	}
	return fingerprint == pwFingerprint(set.PanelPasswordHash)
}

// parseToken 拆出会话令牌里的载荷。
func parseToken(token string) (user string, issued int64, exp int64, fingerprint string, ok bool) {
	raw, _, cut := strings.Cut(token, ".")
	if !cut {
		return "", 0, 0, "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return "", 0, 0, "", false
	}
	fields := strings.Split(string(payload), "|")
	if len(fields) != 5 {
		return "", 0, 0, "", false
	}
	issued, err = strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return "", 0, 0, "", false
	}
	exp, err = strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return "", 0, 0, "", false
	}
	return fields[0], issued, exp, fields[3], true
}

// signatureOK 校验令牌签名，并确认它没被登出作废。
func (a *authGuard) signatureOK(token string) bool {
	raw, sig, ok := strings.Cut(token, ".")
	if !ok {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return false
	}
	want, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, a.key)
	mac.Write(payload)
	return hmac.Equal(want, mac.Sum(nil))
}

// isRevoked 报告这个令牌是不是已经被登出作废。
func (a *authGuard) isRevoked(token string) bool {
	a.mu.Lock()
	until, ok := a.revoked[token]
	a.mu.Unlock()
	return ok && time.Now().Before(until)
}

// signToken 给载荷签名，令牌形如 base64(载荷).base64(签名)。
func signToken(key []byte, payload string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// pwFingerprint 取口令哈希的指纹，用来判断「口令是不是被改过」。
func pwFingerprint(hash string) string {
	if hash == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(hash))
	return base64.RawURLEncoding.EncodeToString(sum[:8])
}

// locked 报告这个来源是不是正被暂时锁定。
func (a *authGuard) locked(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	f, ok := a.failures[ip]
	if !ok {
		return false
	}
	// 从没锁过就别动计数 —— 每次失败都清零的话，
	// 计数永远攒不到阈值，锁定形同虚设。
	if f.until.IsZero() {
		return false
	}
	if time.Now().Before(f.until) {
		return true
	}
	// 锁定时间过了，计数清零重新算。
	f.count = 0
	f.until = time.Time{}
	return false
}

// fail 记一次失败，达到阈值就锁一段时间。
func (a *authGuard) fail(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f, ok := a.failures[ip]
	if !ok {
		f = &loginFail{}
		a.failures[ip] = f
	}
	f.count++
	if f.count >= loginFailMax {
		f.until = time.Now().Add(loginLockFor)
	}
	// 表别无限长：清掉已经解锁的条目。
	if len(a.failures) > maxFailRecords {
		now := time.Now()
		for k, v := range a.failures {
			if v.until.IsZero() || now.After(v.until) {
				delete(a.failures, k)
			}
		}
	}
}

// clearFail 登录成功后清掉计数。
func (a *authGuard) clearFail(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.failures, ip)
}

// sessionToken 从请求里取会话令牌。
func sessionToken(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

// isJSONRequest 判断请求是不是 JSON。
func isJSONRequest(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.EqualFold(strings.TrimSpace(ct), "application/json")
}

// clientIP 取请求来源地址，用来记登录失败次数。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
