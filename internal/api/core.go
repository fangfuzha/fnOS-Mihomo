package api

import (
	"errors"
	"log"
	"net/http"
	"strconv"

	"fnos-mihomo/internal/core"
)

func (s *Server) handleCoreStart(w http.ResponseWriter, r *http.Request) {
	if err := s.ApplyConfig(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// 手动启动视为用户接管，清掉「手动停止」的标记，
	// 之后订阅状态变化时自动启停可以继续跟随。
	s.manualStop = false
	if err := s.core.Start(); err != nil {
		// 已经在跑了就是达成了目的，不算失败。
		if !errors.Is(err, core.ErrAlreadyRunning) {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleCoreStop(w http.ResponseWriter, r *http.Request) {
	if err := s.core.Stop(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// 记住这是用户主动停的，自动启停不再把它拉起来。
	s.manualStop = true
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleCoreRestart(w http.ResponseWriter, r *http.Request) {
	if err := s.ApplyConfig(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.core.Restart(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	n := 200
	if v := r.URL.Query().Get("lines"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 && parsed <= 2000 {
			n = parsed
		}
	}
	writeJSON(w, http.StatusOK, s.core.TailLog(n))
}

// SyncCoreState 对外暴露自动启停，供主程序在启动时调用。
func (s *Server) SyncCoreState() {
	s.syncCoreState()
}

// syncCoreState 让内核的运行状态跟随订阅状态自动变化。
//
// 之前只有用户在界面上手动点「启动内核」才会跑，配置改了也不会自己跟上：
//   - 拉到节点了，内核还停着 → 节点列表空着，什么都用不了
//   - 订阅全删了或全停了，内核还在跑 → 白占着代理端口
//
// 规则很简单：有可用节点就运行，没有就停止。
// 用户手动点的「停止内核」会被记住，不被这里覆盖 ——
// 否则刚点完停止，状态同步又把它拉起来，手动操作就没意义了。
func (s *Server) syncCoreState() {
	// 有可用节点才值得运行内核。
	// 判断依据是本地缓存里真实解析出的节点，不是订阅表里记的 NodeCount。
	hasNodes := false
	if s.HasNodes != nil {
		hasNodes = s.HasNodes()
	} else {
		for _, sb := range s.store.Get().Subscriptions {
			if sb.Enabled && sb.NodeCount > 0 {
				hasNodes = true
				break
			}
		}
	}

	if !hasNodes {
		// 没有节点就把内核停掉。
		if s.core.IsRunning() {
			if err := s.core.Stop(); err != nil {
				log.Printf("自动停止内核失败: %v", err)
			} else {
				log.Printf("没有可用节点，已自动停止内核")
			}
		}
		return
	}

	// 有节点但内核没跑：自动启动。
	// 用户手动停过就先不自动拉起，等他再点一次启动。
	if s.core.IsRunning() || s.manualStop {
		return
	}
	if err := s.ApplyConfig(); err != nil {
		log.Printf("自动启动前生成配置失败: %v", err)
		return
	}
	if err := s.core.Start(); err != nil {
		// 别的协程刚把内核拉起来了，目的已达成，不算失败。
		if !errors.Is(err, core.ErrAlreadyRunning) {
			log.Printf("自动启动内核失败: %v", err)
			return
		}
		log.Printf("检测到可用节点，内核已在运行")
		return
	}
	log.Printf("检测到可用节点，已自动启动内核")
}
