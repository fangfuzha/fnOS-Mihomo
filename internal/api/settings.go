package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"fnos-mihomo/internal/settings"
)

// handleSettings 读取或保存设置。
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		set := s.store.Get()
		// 口令哈希只留在本机，不回传给前端。
		// 控制密钥仍要回传：设置页要显示它，用户才能改。
		set.PanelPasswordHash = ""
		set.PanelPassword = ""
		writeJSON(w, http.StatusOK, set)
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, fmt.Errorf("不支持的方法"))
		return
	}

	// 内嵌 settings.Settings 是为了让前端「读回来改一改再写回去」的用法照旧；
	// panelPassword 与 clearPanelPassword 是这个结构体里没有的两个额外字段，
	// 外层同名字段会盖掉内嵌的那个，所以拿到的是明文口令而不是哈希。
	var incoming struct {
		settings.Settings
		PanelPassword      string `json:"panelPassword"`
		ClearPanelPassword bool   `json:"clearPanelPassword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	if incoming.MixedPort < 1 || incoming.MixedPort > 65535 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("代理端口需在 1-65535 之间"))
		return
	}
	if incoming.ControllerPort < 1 || incoming.ControllerPort > 65535 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("控制端口需在 1-65535 之间"))
		return
	}

	// 口令哈希先算好再落盘：派生要一百毫秒量级，别把配置的锁握着算。
	// 留空表示不改口令 —— 前端每次保存设置都会把整个设置对象写回来，
	// 不区分「没填」和「清空」会把已设的口令抹掉。
	newHash := ""
	switch {
	case incoming.ClearPanelPassword:
		newHash = ""
	case strings.TrimSpace(incoming.PanelPassword) != "":
		h, err := settings.HashPassword(incoming.PanelPassword)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		newHash = h
	}
	user := strings.TrimSpace(incoming.PanelUser)
	if user == "" {
		user = settings.DefaultUser
	}

	// 订阅列表以服务端保存的为准，这里只接受设置项，避免被前端误清空。
	subs := s.store.Get().Subscriptions
	if err := s.store.Update(func(c *settings.Settings) {
		c.MixedPort = incoming.MixedPort
		c.ControllerPort = incoming.ControllerPort
		c.ControllerSecret = incoming.ControllerSecret
		c.AllowLan = incoming.AllowLan
		// ipv6 是指针：老版本前端不传这个字段时，
		// 不该把用户已经设好的值冲掉。
		if incoming.IPv6 != nil {
			c.IPv6 = incoming.IPv6
		}
		c.LogLevel = incoming.LogLevel
		c.Mode = incoming.Mode
		c.RulesMode = incoming.RulesMode
		c.Dashboard = incoming.Dashboard
		c.Gateway = incoming.Gateway
		c.PanelUser = user
		if newHash != "" || incoming.ClearPanelPassword {
			c.PanelPasswordHash = newHash
			c.PanelPassword = ""
		}
		c.Subscriptions = subs
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	if err := s.ApplyConfig(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// 网关处于开启状态时，设置变化（网段、DNS 劫持等）需要重新落规则。
	// ApplyConfig 会重启内核，规则里的 tproxy 端口才与新配置对得上。
	if s.store.Get().Gateway.Enabled {
		if err := s.gateway.Apply(s.store.Get().Gateway); err != nil {
			writeErr(w, http.StatusInternalServerError, fmt.Errorf("重新应用网关规则失败: %w", err))
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
