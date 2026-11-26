package mihomo

import (
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"fnos-mihomo/internal/safeio"
	"fnos-mihomo/internal/settings"
	"fnos-mihomo/internal/sub"
)

// Config 是最终写盘的 mihomo 配置。
type Config struct {
	MixedPort          int          `yaml:"mixed-port"`
	AllowLan           bool         `yaml:"allow-lan"`
	BindAddress        string       `yaml:"bind-address"`
	Mode               string       `yaml:"mode"`
	LogLevel           string       `yaml:"log-level"`
	IPv6               bool         `yaml:"ipv6"`
	ExternalController string       `yaml:"external-controller"`
	ExternalUI         string       `yaml:"external-ui,omitempty"`
	Secret             *string      `yaml:"secret,omitempty"`
	DNS                DNS          `yaml:"dns"`
	Tun                *Tun         `yaml:"tun,omitempty"`
	GeodataMode        bool         `yaml:"geodata-mode"`
	GeoIPPath          string       `yaml:"geoip,omitempty"`
	GeoSitePath        string       `yaml:"geosite,omitempty"`
	MMDBPath           string       `yaml:"mmdb,omitempty"`
	GeoAutoUpdate      bool         `yaml:"geo-auto-update"`
	Proxies            []sub.Proxy  `yaml:"proxies"`
	ProxyGroups        []ProxyGroup `yaml:"proxy-groups"`
	Rules              []string     `yaml:"rules"`
}

type DNS struct {
	Enable                bool            `yaml:"enable"`
	Listen                string          `yaml:"listen,omitempty"`
	EnhancedMode          string          `yaml:"enhanced-mode,omitempty"`
	Nameserver            []string        `yaml:"nameserver,omitempty"`
	Fallback              []string        `yaml:"fallback,omitempty"`
	FallbackFilter        *FallbackFilter `yaml:"fallback-filter,omitempty"`
	DefaultNameserver     []string        `yaml:"default-nameserver,omitempty"`
	ProxyServerNameserver []string        `yaml:"proxy-server-nameserver,omitempty"`
}

type FallbackFilter struct {
	GeoIP     bool   `yaml:"geoip"`
	GeoIPCode string `yaml:"geoip-code,omitempty"`
}

type Tun struct {
	Enable              bool     `yaml:"enable"`
	Stack               string   `yaml:"stack"`
	AutoRoute           bool     `yaml:"auto-route"`
	AutoDetectInterface bool     `yaml:"auto-detect-interface"`
	DNSHijack           []string `yaml:"dns-hijack,omitempty"`
	Device              string   `yaml:"device,omitempty"`
	// Inet6Address 是 TUN 设备自己的 IPv6 地址。
	//
	// 不配这一项，Linux 的 auto-route 就不会把 IPv6 流量引进虚拟网卡：
	// IPv6 会绕过代理直连出去，表现为「开了代理但 IPv6 的站点还是不通」
	// （ping 还能拿到目标真实的 IPv6 地址）。
	Inet6Address []string `yaml:"inet6-address,omitempty"`
}

type ProxyGroup struct {
	Name     string   `yaml:"name"`
	Type     string   `yaml:"type"`
	Proxies  []string `yaml:"proxies,omitempty"`
	URL      string   `yaml:"url,omitempty"`
	Interval int      `yaml:"interval,omitempty"`
	Filter   string   `yaml:"filter,omitempty"`
	Use      []string `yaml:"use,omitempty"`
}

// Build 根据设置与解析结果生成完整配置。
//
// 订阅若自带策略组与规则（Clash YAML），优先采用它们的 —— 机场的分流策略
// 通常比自己重建更贴合用户意图；只有订阅没给的时候才由本应用兜底生成。
func Build(set settings.Settings, parsed *sub.Parsed) (*Config, error) {
	if parsed == nil || len(parsed.Proxies) == 0 {
		return nil, fmt.Errorf("没有可用节点，无法生成配置")
	}

	// 多订阅合并后可能出现同名节点，mihomo 遇到重名直接拒绝启动。
	// 这里统一去重，并记录改名映射以便修正策略组里的引用。
	remap := dedupeNames(parsed.Proxies)

	c := &Config{
		MixedPort:   set.MixedPort,
		AllowLan:    set.AllowLan,
		BindAddress: "*",
		Mode:        set.Mode,
		LogLevel:    set.LogLevel,
		// mihomo 自身的默认值就是 true，这里之前写死成 false，
		// 等于主动关掉了内核的 IPv6 能力。
		IPv6:               set.IPv6Enabled(),
		ExternalController: fmt.Sprintf("0.0.0.0:%d", set.ControllerPort),
		Secret:             secretPtr(set.ControllerSecret),

		Proxies: remap.proxies,
	}

	c.DNS = DNS{
		Enable:       true,
		EnhancedMode: "fake-ip",
		// default-nameserver 用来解析 DNS 服务器本身的域名，必须是纯 IP，
		// 否则会陷入「解析 DNS 服务器 → 又要 DNS → 又要解析 DNS 服务器」的死循环。
		DefaultNameserver: []string{"223.5.5.5", "119.29.29.29"},
		// proxy-server-nameserver 专门用来解析节点服务器的域名。
		// 少了这一项，解析机场域名时会走 fake-ip 的正常流程而拿到假 IP，
		// 表现为 "dns resolve failed: context deadline exceeded"。
		ProxyServerNameserver: []string{"223.5.5.5", "119.29.29.29"},
		Nameserver:            []string{"https://dns.alidns.com/dns-query", "https://doh.pub/dns-query"},
		Fallback:              []string{"https://1.1.1.1/dns-query", "https://dns.google/dns-query"},
		FallbackFilter:        &FallbackFilter{GeoIP: true, GeoIPCode: "CN"},
	}

	if set.Gateway.Enabled {
		// 让 NAS 本机的出站流量走代理。
		//
		// 走 mihomo 的 TUN 模式：它自己创建虚拟网卡并接管路由，
		// 本机进程发出的包全部经过它，再按分流规则决定直连还是走代理。
		//
		// 为什么不用 nftables 的 output 钩子 + tproxy：
		// 内核的 tproxy 表达式只支持 prerouting（入站）钩子，
		// 在 output 钩子会直接报 "Operation not supported"。
		// 而 REDIRECT 会丢掉原始目的地址，mihomo 拿不到真实的目标。
		// TUN 是接管本机出站流量的标准做法。
		c.AllowLan = true
		c.BindAddress = "*"
		c.Tun = &Tun{
			Enable:              true,
			Stack:               "mixed",
			AutoRoute:           true,
			AutoDetectInterface: true,
			Device:              "Mihomo0",
		}
		// DNS 一并交给 TUN 接管：本机域名解析也走代理，避免解析到污染地址。
		c.Tun.DNSHijack = []string{"any:53"}

		if set.IPv6Enabled() {
			// 给 TUN 设备一个 IPv6 地址，auto-route 才会把 IPv6 流量也引进来。
			//
			// 刻意不开 strict-route：那个选项会让所有流量（包括局域网）
			// 都必须经过 TUN，内核一挂 NAS 就连后台都进不去了。
			// 现在这种「默认路由劫持 + 局域网走更具体路由」的方式，
			// 内核挂了局域网访问不受影响。
			c.Tun.Inet6Address = []string{"fdfe:dcba:9876::1/126"}
		}
	}

	// geodata 走随包分发的本地文件。
	// 国内 NAS 基本连不上 GitHub releases，让内核自己去下载必然失败。
	if set.GeoDir != "" {
		// geodata-mode: true 时内核用 geoip.dat / geosite.dat；
		// 这里直接把绝对路径写进配置，不依赖它去哪里找文件。
		c.GeodataMode = true
		c.GeoIPPath = filepath.Join(set.GeoDir, "geoip.dat")
		c.GeoSitePath = filepath.Join(set.GeoDir, "geosite.dat")
		c.GeoAutoUpdate = false
	}

	// 第三方面板：面板文件由面板进程下载到 <dataDir>/ui，
	// 内核只负责把本地面板托管在控制端口的 /ui/ 下。
	//
	// 这里刻意不写 external-ui-url：内核自己的下载器会套用分流规则，
	// 机场规则里 github.com 通常走代理，节点一慢或挂掉面板就下不下来
	// （实测报 EOF，表现是面板永远是 404）。
	//
	// 留空表示不用第三方面板，这时连 external-ui 都不写，
	// 内核不会去托管任何 UI。
	if set.Dashboard != "" {
		c.ExternalUI = "ui"
	}

	// 策略组与规则。
	c.ProxyGroups, c.Rules = buildGroupsAndRules(set, parsed, remap.rename)
	return c, nil
}

// buildGroupsAndRules 决定用订阅自带的策略组还是本应用生成的。
func buildGroupsAndRules(set settings.Settings, parsed *sub.Parsed, rename map[string]string) ([]ProxyGroup, []string) {
	if parsed.Clash && len(parsed.Groups) > 0 {
		groups := make([]ProxyGroup, 0, len(parsed.Groups))
		for _, g := range parsed.Groups {
			groups = append(groups, ProxyGroup{
				Name:     g.Name,
				Type:     g.Type,
				Proxies:  applyRename(g.Proxies, rename),
				URL:      g.URL,
				Interval: g.Interval,
				Filter:   g.Filter,
				Use:      g.Use,
			})
		}
		// 规则用订阅的，但要把内网直连规则补在最前面，
		// 否则 NAS 自己的服务可能被机场规则绕进代理里。
		rules := append(lanRules(), parsed.Rules...)
		return groups, rules
	}

	// 订阅没给策略组：由本应用生成三个基础组。
	names := make([]string, 0, len(parsed.Proxies))
	for _, p := range parsed.Proxies {
		names = append(names, p.Name)
	}

	groups := []ProxyGroup{
		{
			Name:    "🚀 节点选择",
			Type:    "select",
			Proxies: append([]string{"♻️ 自动选择", "🐟 故障转移"}, names...),
		},
		{
			Name:     "♻️ 自动选择",
			Type:     "url-test",
			URL:      "http://www.gstatic.com/generate_204",
			Interval: 300,
			Proxies:  names,
		},
		{
			Name:     "🐟 故障转移",
			Type:     "fallback",
			URL:      "http://www.gstatic.com/generate_204",
			Interval: 300,
			Proxies:  names,
		},
	}

	return groups, buildRules(set.RulesMode)
}

// lanRules 是内网直连规则，永远放在最前。
func lanRules() []string {
	return []string{
		"IP-CIDR,127.0.0.0/8,DIRECT,no-resolve",
		"IP-CIDR,10.0.0.0/8,DIRECT,no-resolve",
		"IP-CIDR,172.16.0.0/12,DIRECT,no-resolve",
		"IP-CIDR,192.168.0.0/16,DIRECT,no-resolve",
		"IP-CIDR,169.254.0.0/16,DIRECT,no-resolve",
		"IP-CIDR6,::1/128,DIRECT,no-resolve",
		"IP-CIDR6,fc00::/7,DIRECT,no-resolve",
		"IP-CIDR6,fe80::/10,DIRECT,no-resolve",
	}
}

// buildRules 根据分流模式生成规则表（订阅未提供规则时使用）。
func buildRules(mode string) []string {
	rules := lanRules()

	switch mode {
	case "global":
		rules = append(rules, "MATCH,🚀 节点选择")
	case "direct":
		rules = append(rules, "MATCH,DIRECT")
	default:
		// balanced：国内直连、国外走代理。
		rules = append(rules,
			"GEOIP,CN,DIRECT",
			"GEOSITE,geolocation-!cn,🚀 节点选择",
			"MATCH,🚀 节点选择",
		)
	}

	return rules
}

// ProxyNames 返回节点名列表，供界面展示。
func ProxyNames(proxies []sub.Proxy) []string {
	names := make([]string, 0, len(proxies))
	for _, p := range proxies {
		names = append(names, p.Name)
	}
	return names
}

// SanitizeGroupName 去掉策略组名里的特殊字符，避免 YAML 解析出问题。
func SanitizeGroupName(s string) string {
	return strings.NewReplacer(":", "", "\n", " ").Replace(s)
}

// secretPtr 空密钥返回 nil，让 YAML 完全不输出 secret 字段。
// 直接写空字符串的话 yaml.v3 会输出 secret: ”””，
// mihomo 读到后会把六个单引号当成真正的密钥，认证就废了。
func secretPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// remapResult 保存去重后的节点列表与改名映射。
type remapResult struct {
	proxies []sub.Proxy
	rename  map[string]string
}

// dedupeNames 给重名节点加后缀，并返回 原名->新名 的映射。
// 多个订阅合并后出现同名节点时，mihomo 会因重名直接拒绝启动。
func dedupeNames(proxies []sub.Proxy) remapResult {
	used := map[string]int{}
	rename := map[string]string{}
	out := make([]sub.Proxy, len(proxies))
	copy(out, proxies)

	for i := range out {
		name := out[i].Name
		if n, ok := used[name]; ok {
			used[name] = n + 1
			newName := fmt.Sprintf("%s-%d", name, n+1)
			// 只有第一次改名才记录映射，否则策略组里多个同名引用会指向同一个新名。
			if _, recorded := rename[name]; !recorded {
				rename[name] = newName
			}
			out[i].Name = newName
		} else {
			used[name] = 1
		}
	}

	return remapResult{proxies: out, rename: rename}
}

// WriteConfig 把配置写成 YAML 并原子替换目标文件。
//
// 先写临时文件再改名：内核正在读配置时不会读到半份文件。
// 临时文件名每次唯一 —— 启动时后台刷新订阅与自动启停会同时写这里，
// 共用固定的 .tmp 会撞名。
func WriteConfig(path string, cfg *Config) error {
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return safeio.WriteFile(path, b, 0o600)
}

// applyRename 按映射修正策略组里的节点引用。
func applyRename(names []string, rename map[string]string) []string {
	if len(rename) == 0 {
		return names
	}
	out := make([]string, len(names))
	for i, n := range names {
		if v, ok := rename[n]; ok {
			out[i] = v
		} else {
			out[i] = n
		}
	}
	return out
}
