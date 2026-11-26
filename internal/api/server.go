package api

import (
	"encoding/json"
	"net/http"
	"time"

	"fnos-mihomo/internal/settings"
)

// coreController 是内核控制器的最小接口。
//
// 用接口而不是具体类型，是为了能注入假实现写测试 ——
// 内核管理器要真的起进程，单测里没法用。
type coreController interface {
	IsRunning() bool
	Start() error
	Stop() error
	Restart() error
	Uptime() time.Duration
	TailLog(n int) []string
	Version() string
}

// gatewayController 是接管层的最小接口。
//
// 同上：接管会动整机路由，单测里只能拿假实现顶上。
type gatewayController interface {
	Apply(cfg settings.GatewayConfig) error
	Remove() error
	Verify() error
	Status() bool
}

// Server 对外提供面板 REST API 与静态页面。
type Server struct {
	store   *settings.Store
	core    coreController
	gateway gatewayController

	// auth 管面板的登录会话。
	auth *authGuard

	// FetchSubscription 拉取订阅正文，由主程序注入以便测试与复用。
	FetchSubscription func(url string) (string, error)
	// ApplyConfig 把生成的配置写盘，由主程序注入。
	ApplyConfig func() error
	// HasNodes 判断当前是否有真正可用的节点，由主程序注入。
	HasNodes func() bool
	// DashboardStatus 返回第三方面板的下载状态与错误信息，由主程序注入。
	DashboardStatus func() (string, string)

	// manualStop 记录用户是否手动点过「停止内核」。
	// 手动停止的意图不该被自动启停覆盖 —— 点了停，结果又被拉起来，
	// 那手动操作就没有意义了。
	manualStop bool
}

// NewServer 装配 API 层。
func NewServer(store *settings.Store, c coreController, gw gatewayController) *Server {
	return &Server{store: store, core: c, gateway: gw, auth: newAuthGuard(store)}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

// Register 注册所有 API 路由。
func (s *Server) Register(mux *http.ServeMux) {
	// 登录相关的接口不受鉴权保护 —— 不然就没人能登录了。
	mux.HandleFunc("/api/auth", s.handleAuthState)
	mux.HandleFunc("/api/login", s.handleLogin)
	// 登出也要鉴权：没鉴权的话，同网段任何人都能把管理员踢下线。
	mux.HandleFunc("/api/logout", s.authed(s.handleLogout))

	// 其余接口都要过鉴权：设了面板口令后，
	// 同网段的人不能直接读到订阅链接、改设置、启停内核。
	mux.HandleFunc("/api/status", s.authed(s.handleStatus))
	mux.HandleFunc("/api/settings", s.authed(s.handleSettings))
	mux.HandleFunc("/api/subscriptions", s.authed(s.handleSubscriptions))
	mux.HandleFunc("/api/subscriptions/update", s.authed(s.handleSubscriptionsUpdate))
	mux.HandleFunc("/api/subscriptions/delete", s.authed(s.handleSubscriptionsDelete))
	mux.HandleFunc("/api/core/start", s.authed(s.handleCoreStart))
	mux.HandleFunc("/api/core/stop", s.authed(s.handleCoreStop))
	mux.HandleFunc("/api/core/restart", s.authed(s.handleCoreRestart))
	mux.HandleFunc("/api/logs", s.authed(s.handleLogs))
	mux.HandleFunc("/api/proxies", s.authed(s.handleProxies))
	mux.HandleFunc("/api/proxies/", s.authed(s.handleProxySelect))
	mux.HandleFunc("/api/gateway/toggle", s.authed(s.handleGatewayToggle))
	mux.HandleFunc("/api/gateway/status", s.authed(s.handleGatewayStatus))
	mux.HandleFunc("/api/dashboard", s.authed(s.handleDashboard))
}

// handleStatus 返回面板需要的全部状态。
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	set := s.store.Get()
	writeJSON(w, http.StatusOK, map[string]any{
		"running":           s.core.IsRunning(),
		"uptime":            s.core.Uptime().String(),
		"version":           s.core.Version(),
		"mixedPort":         set.MixedPort,
		"controllerPort":    set.ControllerPort,
		"mode":              set.Mode,
		"allowLan":          set.AllowLan,
		"gatewayEnabled":    set.Gateway.Enabled,
		"gatewayActive":     s.gateway.Status(),
		"subscriptionCount": len(set.Subscriptions),
		"enabledCount":      countEnabled(set.Subscriptions),
	})
}

func countEnabled(subs []settings.Subscription) int {
	n := 0
	for _, s := range subs {
		if s.Enabled {
			n++
		}
	}
	return n
}
