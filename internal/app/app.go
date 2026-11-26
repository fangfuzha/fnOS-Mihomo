// Package app 汇集面板的业务逻辑。
//
// cmd/panel 只负责装配与启停，真正的活都在这里：订阅的拉取与缓存、
// mihomo 配置的生成、内核状态的对齐。
//
// 放在包里的理由很直接 —— main 包里的函数没法写单测，
// 而这些逻辑恰恰是最容易出岔子的地方（合并订阅的规则、
// 向导配置怎么套用、面板装到哪一版了）。
package app

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"fnos-mihomo/internal/dashboard"
	"fnos-mihomo/internal/mihomo"
	"fnos-mihomo/internal/safeio"
	"fnos-mihomo/internal/settings"
	"fnos-mihomo/internal/sub"
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
}

// Service 是面板的业务逻辑集合。
//
// 它持有设置存储、内核管理器与第三方面板缓存；
// api 层通过它来拉订阅、生成配置、查面板状态。
type Service struct {
	store   *settings.Store
	core    coreController
	dataDir string
	dash    *dashboard.Cache

	// dashState 记录第三方面板的下载状态。
	dashState dashboardState
}

// NewService 装配业务逻辑。dataDir 是数据目录（配置、日志、节点缓存）。
func NewService(store *settings.Store, coreMgr coreController, dataDir string, dash *dashboard.Cache) *Service {
	return &Service{
		store:   store,
		core:    coreMgr,
		dataDir: dataDir,
		dash:    dash,
	}
}

// FetchSubscription 拉取订阅正文。
//
// 订阅站按 User-Agent 返回不同格式，这里声明自己是 Clash.Meta，
// 以拿到节点链接列表。
func (s *Service) FetchSubscription(subURL string) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest(http.MethodGet, subURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "clash.meta")
	req.Header.Set("Accept", "*/*")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("订阅返回 HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// ApplyConfig 汇总所有已启用订阅的节点，生成并写入 mihomo 配置。
//
// 多份订阅合并时：节点直接拼接；策略组与规则取第一份带策略组的订阅（Clash YAML），
// 其余订阅只贡献节点。这样机场自带的分流策略不会被自建规则冲掉。
func (s *Service) ApplyConfig() error {
	set := s.store.Get()

	// 第三方面板由面板进程自己准备，不交给内核的 external-ui-url：
	// 内核的下载器套用分流规则，机场规则里 github.com 走代理，
	// 节点一慢面板就下不下来（实测报 EOF，面板永远是 404）。
	//
	// 归档优先用本地那份（随包分发或之前缓存的），
	// 只有本地没有或上游出了新版本才去 GitHub 拉。
	//
	// 放后台跑：首次安装要十几秒，不能挡住面板启动。
	s.EnsureDashboard(set.Dashboard, fmt.Sprintf("http://127.0.0.1:%d", set.MixedPort))

	var all []sub.Proxy
	var merged *sub.Parsed

	for _, sb := range set.Subscriptions {
		if !sb.Enabled {
			continue
		}
		parsed, err := s.loadCachedProxies(sb.ID)
		if err != nil || parsed == nil || len(parsed.Proxies) == 0 {
			// 缓存不存在或为空时现拉一次。
			body, ferr := s.FetchSubscription(sb.URL)
			if ferr != nil {
				log.Printf("订阅 %s 拉取失败: %v", sb.Name, ferr)
				continue
			}
			parsed, ferr = sub.ParseSubscription(body)
			if ferr != nil {
				log.Printf("订阅 %s 解析失败: %v", sb.Name, ferr)
				continue
			}
			if err := s.saveCachedProxies(sb.ID, parsed); err != nil {
				log.Printf("缓存订阅 %s 失败: %v", sb.Name, err)
			}
		}
		all = append(all, parsed.Proxies...)
		if merged == nil {
			merged = parsed
		} else if !merged.Clash && parsed.Clash {
			// 后来的这份带完整策略组，优先采用它的。
			merged.Groups = parsed.Groups
			merged.Rules = parsed.Rules
			merged.Clash = true
		}
	}

	configPath := filepath.Join(s.dataDir, "config.yaml")
	if len(all) == 0 {
		// 没有节点时仍然写一份最小配置，避免内核因为配置文件不存在而无法启动。
		minCfg := &mihomo.Config{
			MixedPort:          set.MixedPort,
			AllowLan:           set.AllowLan,
			BindAddress:        "*",
			Mode:               set.Mode,
			LogLevel:           set.LogLevel,
			ExternalController: fmt.Sprintf("0.0.0.0:%d", set.ControllerPort),
			Secret:             nil,
			Proxies:            []sub.Proxy{},
			ProxyGroups:        []mihomo.ProxyGroup{},
			Rules:              []string{"MATCH,DIRECT"},
		}
		return mihomo.WriteConfig(configPath, minCfg)
	}

	cfg, err := mihomo.Build(set, merged)
	if err != nil {
		return err
	}

	if err := mihomo.WriteConfig(configPath, cfg); err != nil {
		return err
	}

	// 内核正在运行就重启，让新配置立即生效。
	if s.core.IsRunning() {
		if err := s.core.Restart(); err != nil {
			return fmt.Errorf("重启内核失败: %w", err)
		}
	}
	return nil
}

// HasNodes 判断当前是否有真的可用节点。
//
// 以本地缓存里实际解析出的节点为准，而不是订阅表里记的 NodeCount ——
// 后者只是上次拉取时的数字，缓存文件丢了它也不会变。
func (s *Service) HasNodes() bool {
	for _, sb := range s.store.Get().Subscriptions {
		if !sb.Enabled {
			continue
		}
		parsed, err := s.loadCachedProxies(sb.ID)
		if err != nil || parsed == nil || len(parsed.Proxies) == 0 {
			continue
		}
		return true
	}
	return false
}

// RefreshSubscriptions 后台拉取所有启用但节点为空的订阅。
//
// afterRefresh 是刷新结束后的回调，用来对齐内核的运行状态 ——
// 刷新可能补上了节点，这时内核应该自动起来。
func (s *Service) RefreshSubscriptions(afterRefresh func()) {
	set := s.store.Get()
	for _, sb := range set.Subscriptions {
		if !sb.Enabled || sb.NodeCount > 0 {
			continue
		}
		log.Printf("后台刷新订阅: %s", sb.Name)

		body, err := s.FetchSubscription(sb.URL)
		if err != nil {
			log.Printf("订阅 %s 拉取失败: %v", sb.Name, err)
			continue
		}
		parsed, err := sub.ParseSubscription(body)
		if err != nil {
			log.Printf("订阅 %s 解析失败: %v", sb.Name, err)
			continue
		}
		if err := s.saveCachedProxies(sb.ID, parsed); err != nil {
			log.Printf("缓存订阅 %s 失败: %v", sb.Name, err)
		}

		_ = s.store.Update(func(c *settings.Settings) {
			for i := range c.Subscriptions {
				if c.Subscriptions[i].ID == sb.ID {
					c.Subscriptions[i].NodeCount = len(parsed.Proxies)
					c.Subscriptions[i].UpdatedAt = time.Now().Format(time.RFC3339)
					c.Subscriptions[i].LastError = ""
				}
			}
		})
		log.Printf("订阅 %s 刷新完成，共 %d 个节点", sb.Name, len(parsed.Proxies))
	}

	// 节点补齐后重新生成配置并重启内核。
	if err := s.ApplyConfig(); err != nil {
		log.Printf("刷新后生成配置失败: %v", err)
	}

	if afterRefresh != nil {
		afterRefresh()
	}
}

// proxyCachePath 订阅解析结果在数据目录里的位置。
func (s *Service) proxyCachePath(id string) string {
	return filepath.Join(s.dataDir, "subs", id+".json")
}

// saveCachedProxies 把解析结果写到磁盘。
func (s *Service) saveCachedProxies(id string, parsed *sub.Parsed) error {
	path := s.proxyCachePath(id)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(parsed)
	if err != nil {
		return err
	}
	// 原子写入：订阅刷新与面板操作可能同时写同一个缓存文件。
	return safeio.WriteFile(path, b, 0o600)
}

// loadCachedProxies 读磁盘上缓存的解析结果。
func (s *Service) loadCachedProxies(id string) (*sub.Parsed, error) {
	b, err := os.ReadFile(s.proxyCachePath(id))
	if err != nil {
		return nil, err
	}
	var parsed sub.Parsed
	if err := json.Unmarshal(b, &parsed); err != nil {
		return nil, err
	}
	return &parsed, nil
}
