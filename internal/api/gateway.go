package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"fnos-mihomo/internal/settings"
)

// handleGatewayToggle 开启或关闭 NAS 本机出站流量接管。
func (s *Server) handleGatewayToggle(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if in.Enabled == nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("缺少 enabled 参数"))
		return
	}

	// 先把开关写进设置，再重新生成配置 —— 配置里才有正确的 tun 段。
	// 顺序反了的话，内核会拿着没有 tun 的旧配置重启，接管不会生效。
	if err := s.store.Update(func(c *settings.Settings) {
		c.Gateway.Enabled = *in.Enabled
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	if err := s.ApplyConfig(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	// 内核可能本来就没在运行 —— applyConfig 只在它运行时才重启。
	// 接管要生效，内核必须在跑，所以这里确保它起来。
	if *in.Enabled && !s.core.IsRunning() {
		if err := s.core.Start(); err != nil {
			writeErr(w, http.StatusInternalServerError, fmt.Errorf("启动内核失败: %w", err))
			return
		}
	}

	// ApplyConfig 已经重启过内核，这里再确认一次接管状态。
	if *in.Enabled {
		if err := s.gateway.Apply(s.store.Get().Gateway); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		// 接管会改动整机路由，必须验证是否真的生效。
		// 失败就自动撤销，不能让 NAS 处于上不了网的半死状态。
		if err := s.gateway.Verify(); err != nil {
			_ = s.store.Update(func(c *settings.Settings) {
				c.Gateway.Enabled = false
			})
			_ = s.ApplyConfig()
			_ = s.gateway.Remove()
			writeErr(w, http.StatusInternalServerError, fmt.Errorf("接管失败，已自动撤销: %w", err))
			return
		}
	} else {
		if err := s.gateway.Remove(); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleGatewayStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"active":  s.gateway.Status(),
		"enabled": s.store.Get().Gateway.Enabled,
	})
}
