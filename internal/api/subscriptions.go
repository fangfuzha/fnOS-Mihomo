package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fnos-mihomo/internal/settings"
	"fnos-mihomo/internal/sub"
)

// handleSubscriptions 列出订阅。
func (s *Server) handleSubscriptions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Get().Subscriptions)
}

// handleSubscriptionsUpdate 新增或更新一个订阅，并立即尝试拉取。
func (s *Server) handleSubscriptionsUpdate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		URL     string `json:"url"`
		Enabled *bool  `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(in.URL) == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("订阅链接不能为空"))
		return
	}

	set := s.store.Get()
	idx := -1
	for i, sub := range set.Subscriptions {
		if in.ID != "" && sub.ID == in.ID {
			idx = i
			break
		}
	}

	name := in.Name
	if name == "" {
		name = "订阅 " + strconv.Itoa(len(set.Subscriptions)+1)
	}

	entry := settings.Subscription{
		ID:      in.ID,
		Name:    name,
		URL:     in.URL,
		Enabled: true,
	}
	if in.Enabled != nil {
		entry.Enabled = *in.Enabled
	}

	if idx >= 0 {
		entry.ID = set.Subscriptions[idx].ID
		entry.Enabled = set.Subscriptions[idx].Enabled
		if in.Enabled != nil {
			entry.Enabled = *in.Enabled
		}
		set.Subscriptions[idx] = entry
	} else {
		entry.ID = fmt.Sprintf("sub-%d", time.Now().UnixNano())
		set.Subscriptions = append(set.Subscriptions, entry)
	}

	if err := s.store.Update(func(c *settings.Settings) {
		c.Subscriptions = set.Subscriptions
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	// 立刻拉取一次，让用户马上看到节点数量或错误信息。
	count, err := s.fetchAndRefresh(entry.ID)
	if err != nil {
		writeErr(w, http.StatusOK, err)
		return
	}
	// 订阅状态变了，内核的运行状态要跟着变。
	s.syncCoreState()
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "nodeCount": count})
}

// handleSubscriptionsDelete 删除订阅。
func (s *Server) handleSubscriptionsDelete(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	subs := s.store.Get().Subscriptions
	kept := make([]settings.Subscription, 0, len(subs))
	for _, sb := range subs {
		if sb.ID != in.ID {
			kept = append(kept, sb)
		}
	}

	if err := s.store.Update(func(c *settings.Settings) {
		c.Subscriptions = kept
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	_ = s.ApplyConfig()
	// 订阅状态变了，内核的运行状态要跟着变。
	s.syncCoreState()
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// fetchAndRefresh 拉取指定订阅并刷新配置。
func (s *Server) fetchAndRefresh(id string) (int, error) {
	set := s.store.Get()
	var target *settings.Subscription
	for i := range set.Subscriptions {
		if set.Subscriptions[i].ID == id {
			target = &set.Subscriptions[i]
			break
		}
	}
	if target == nil {
		return 0, fmt.Errorf("订阅不存在")
	}

	body, err := s.FetchSubscription(target.URL)
	if err != nil {
		_ = s.store.Update(func(c *settings.Settings) {
			for i := range c.Subscriptions {
				if c.Subscriptions[i].ID == id {
					c.Subscriptions[i].LastError = err.Error()
				}
			}
		})
		return 0, fmt.Errorf("拉取订阅失败: %w", err)
	}

	parsed, err := sub.ParseSubscription(body)
	if err != nil {
		_ = s.store.Update(func(c *settings.Settings) {
			for i := range c.Subscriptions {
				if c.Subscriptions[i].ID == id {
					c.Subscriptions[i].LastError = err.Error()
				}
			}
		})
		return 0, err
	}

	// 把节点缓存到文件，后续生成配置时直接读取。
	if err := s.store.Update(func(c *settings.Settings) {
		for i := range c.Subscriptions {
			if c.Subscriptions[i].ID == id {
				c.Subscriptions[i].NodeCount = len(parsed.Proxies)
				c.Subscriptions[i].UpdatedAt = time.Now().Format(time.RFC3339)
				c.Subscriptions[i].LastError = ""
			}
		}
	}); err != nil {
		return 0, err
	}

	if err := s.ApplyConfig(); err != nil {
		return len(parsed.Proxies), err
	}
	return len(parsed.Proxies), nil
}
