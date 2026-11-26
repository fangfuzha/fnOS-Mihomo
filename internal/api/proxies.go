package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// handleProxies 代理 mihomo 的 /proxies 接口，用于展示节点与当前选择。
func (s *Server) handleProxies(w http.ResponseWriter, r *http.Request) {
	set := s.store.Get()
	target := fmt.Sprintf("http://127.0.0.1:%d/proxies", set.ControllerPort)
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if set.ControllerSecret != "" {
		req.Header.Set("Authorization", "Bearer "+set.ControllerSecret)
	}

	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		writeErr(w, http.StatusOK, fmt.Errorf("内核控制接口未就绪: %w", err))
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

// handleProxySelect 切换策略组当前选择的节点。
func (s *Server) handleProxySelect(w http.ResponseWriter, r *http.Request) {
	group := strings.TrimPrefix(r.URL.Path, "/api/proxies/")
	group, err := url.PathUnescape(group)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	var in struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if in.Name == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("缺少节点名称"))
		return
	}

	set := s.store.Get()
	target := fmt.Sprintf("http://127.0.0.1:%d/proxies/%s", set.ControllerPort, url.PathEscape(group))
	body, _ := json.Marshal(map[string]string{"name": in.Name})
	req, err := http.NewRequest(http.MethodPut, target, bytes.NewReader(body))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if set.ControllerSecret != "" {
		req.Header.Set("Authorization", "Bearer "+set.ControllerSecret)
	}

	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		writeErr(w, http.StatusOK, fmt.Errorf("内核控制接口未就绪: %w", err))
		return
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		writeErr(w, http.StatusBadGateway, fmt.Errorf("内核返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(out))))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
