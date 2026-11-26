package settings

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"fnos-mihomo/internal/safeio"
)

// Settings 是面板与内核共用的全部可配置项，持久化在 config.json。
type Settings struct {
	Subscriptions []Subscription `json:"subscriptions"`

	MixedPort        int    `json:"mixedPort"`
	ControllerPort   int    `json:"controllerPort"`
	ControllerSecret string `json:"controllerSecret"`
	AllowLan         bool   `json:"allowLan"`
	LogLevel         string `json:"logLevel"`
	Mode             string `json:"mode"`

	// IPv6 控制内核要不要接管 IPv6 流量。
	//
	// 用指针而不是 bool：老版本的 config.json 里没有这个字段，
	// 得靠 nil 区分「没设置过」和「用户明确关掉了」。
	// nil 一律按开启处理 —— mihomo 自身的默认值也是 true。
	IPv6 *bool `json:"ipv6"`

	Gateway GatewayConfig `json:"gateway"`

	RulesMode string `json:"rulesMode"`

	// Dashboard 是第三方面板的选择（metacubexd / zashboard / yacd）。
	// 留空表示不使用第三方面板，此时只显示本应用自带的精简界面。
	Dashboard string `json:"dashboard"`

	// GeoDir 是 geodata 文件所在目录（随应用包分发）。
	GeoDir string `json:"geoDir"`

	// PanelUser 是面板登录用户名，留空按 admin 处理。
	PanelUser string `json:"panelUser,omitempty"`

	// PanelPassword 是旧版本存明文口令的字段。
	// 读配置时会把它迁移成哈希并清空 —— 留着只为兼容已经写进 config.json 的明文。
	PanelPassword string `json:"panelPassword,omitempty"`

	// PanelPasswordHash 是面板口令的 PBKDF2 哈希。
	// 只在本机保存，对外返回设置时会被抹掉。
	PanelPasswordHash string `json:"panelPasswordHash,omitempty"`
}

type Subscription struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Enabled   bool   `json:"enabled"`
	UpdatedAt string `json:"updatedAt"`
	NodeCount int    `json:"nodeCount"`
	LastError string `json:"lastError"`
}

// GatewayConfig 描述「NAS 本机出站流量走代理」的接管方式。
//
// 只接管本机进程发出的流量，不影响局域网客户端，
// 所以客户端不需要改网关设置。
type GatewayConfig struct {
	Enabled bool `json:"enabled"`
}

func Default() *Settings {
	ipv6 := true
	return &Settings{
		Subscriptions:    []Subscription{},
		MixedPort:        7890,
		ControllerPort:   9090,
		ControllerSecret: "",
		AllowLan:         false,
		LogLevel:         "info",
		Mode:             "rule",
		IPv6:             &ipv6,
		RulesMode:        "balanced",
		Dashboard:        "metacubexd",
		PanelUser:        DefaultUser,
		Gateway: GatewayConfig{
			Enabled: false,
		},
	}
}

// DefaultUser 是没设置用户名时使用的默认登录名。
const DefaultUser = "admin"

// AuthEnabled 报告面板是否启用了登录口令。
// 没有口令时保持免登录：内网自用不该被一道登录框挡住。
func (s Settings) AuthEnabled() bool {
	return strings.TrimSpace(s.PanelPasswordHash) != ""
}

// EffectiveUser 返回实际使用的登录用户名，没设就用 admin。
func (s Settings) EffectiveUser() string {
	if u := strings.TrimSpace(s.PanelUser); u != "" {
		return u
	}
	return DefaultUser
}

// IPv6Enabled 返回是否启用 IPv6 代理。
//
// 没设置过（nil）按开启算：mihomo 自身的默认值也是 true，
// 老配置升级后不该莫名其妙把 IPv6 关掉。
func (s Settings) IPv6Enabled() bool {
	return s.IPv6 == nil || *s.IPv6
}

// Store 负责配置的安全读写。
type Store struct {
	mu   sync.RWMutex
	path string
	set  *Settings

	// migrated 表示读取配置时做过迁移（比如明文口令换成了哈希），
	// 需要重新落盘一次，别让旧内容继续留在文件里。
	migrated bool

	// fresh 表示这个 Store 是对着一个还不存在的配置文件建的（首次运行）。
	// 首次运行才套用向导值、生成控制密钥；
	// 用户后来自己清空的口令与密钥不会被重新塞回去。
	fresh bool
}

func NewStore(path string) *Store {
	s := &Store{path: path, set: Default()}
	// 配置文件原先不存在 = 首次运行。
	// 必须在 load 之前记下来：load 可能触发落盘，之后文件就存在了。
	_, statErr := os.Stat(path)
	s.fresh = statErr != nil
	s.load()
	if s.migrated {
		// 迁移只改了内存里的状态，落盘后明文才真正从文件里消失。
		_ = s.Save()
	}
	return s
}

func (s *Store) load() {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	set := Default()
	if err := json.Unmarshal(b, set); err != nil {
		return
	}
	if set.Subscriptions == nil {
		set.Subscriptions = []Subscription{}
	}
	// 老版本配置里没有 dashboard 字段，补上默认值。
	// 不补的话面板会退化成「不使用第三方面板」，用户升级后看不到任何变化。
	if set.Dashboard == "" {
		set.Dashboard = Default().Dashboard
	}
	// 旧版本把面板口令明文写在 panelPassword 里，迁移成哈希并抹掉明文。
	if set.PanelPassword != "" {
		if set.PanelPasswordHash == "" {
			h, err := HashPassword(set.PanelPassword)
			if err != nil {
				// 派生失败就别抹明文：留着它用户还能重设，
				// 总比悄悄退回免登录强。也不落盘。
				log.Printf("迁移面板口令失败，保留明文未清除: %v", err)
				s.set = set
				return
			}
			set.PanelPasswordHash = h
		}
		set.PanelPassword = ""
		s.migrated = true
	}
	s.set = set
}

// Save 落盘。写临时文件再改名，避免进程被杀时留下半截配置。
//
// 用写锁而不是读锁：两个 Save 并发时，除了临时文件会撞名，
// 还可能让旧的状态后改名，把新的状态盖掉。
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.MarshalIndent(s.set, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	return safeio.WriteFile(s.path, b, 0o600)
}

// Get 返回配置副本，调用方随便改都不会影响内部状态。
func (s *Store) Get() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := *s.set
	c.Subscriptions = append([]Subscription(nil), s.set.Subscriptions...)
	return c
}

// Update 修改配置并立即落盘。
func (s *Store) Update(fn func(*Settings)) error {
	s.mu.Lock()
	fn(s.set)
	s.mu.Unlock()
	return s.Save()
}

// Path 返回配置文件路径，供外部判断是否已初始化。
func (s *Store) Path() string {
	return s.path
}

// IsFresh 报告这个配置是不是首次运行（建这个 Store 时配置文件还不存在）。
func (s *Store) IsFresh() bool {
	return s.fresh
}
