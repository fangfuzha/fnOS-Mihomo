package sub

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Proxy 是 mihomo 配置里 proxies 数组的一项。
// 只保留各协议通用与常见字段，omitempty 保证无关字段不写出去。
type Proxy struct {
	Name              string         `yaml:"name" json:"name"`
	Type              string         `yaml:"type" json:"type"`
	Server            string         `yaml:"server" json:"server"`
	Port              int            `yaml:"port" json:"port"`
	Cipher            string         `yaml:"cipher,omitempty" json:"cipher,omitempty"`
	Password          string         `yaml:"password,omitempty" json:"password,omitempty"`
	UUID              string         `yaml:"uuid,omitempty" json:"uuid,omitempty"`
	AlterID           int            `yaml:"alterId,omitempty" json:"alterId,omitempty"`
	Network           string         `yaml:"network,omitempty" json:"network,omitempty"`
	TLS               bool           `yaml:"tls,omitempty" json:"tls,omitempty"`
	SkipCertVerify    bool           `yaml:"skip-cert-verify,omitempty" json:"skip-cert-verify,omitempty"`
	ServerName        string         `yaml:"servername,omitempty" json:"servername,omitempty"`
	ALPN              []string       `yaml:"alpn,omitempty" json:"alpn,omitempty"`
	WSOpts            *WSOpts        `yaml:"ws-opts,omitempty" json:"ws-opts,omitempty"`
	GRPCOpts          *GRPCOpts      `yaml:"grpc-opts,omitempty" json:"grpc-opts,omitempty"`
	Hysteria2Opts     *Hysteria2Opts `yaml:"hysteria2-opts,omitempty" json:"hysteria2-opts,omitempty"`
	RealityOpts       *RealityOpts   `yaml:"reality-opts,omitempty" json:"reality-opts,omitempty"`
	ClientFingerprint string         `yaml:"client-fingerprint,omitempty" json:"client-fingerprint,omitempty"`
	Flow              string         `yaml:"flow,omitempty" json:"flow,omitempty"`
	UDP               bool           `yaml:"udp,omitempty" json:"udp,omitempty"`
}

type WSOpts struct {
	Path    string            `yaml:"path,omitempty" json:"path,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
}

type GRPCOpts struct {
	GRPCServiceName string `yaml:"grpc-service-name,omitempty" json:"grpc-service-name,omitempty"`
}

type Hysteria2Opts struct {
	UpMbps   int    `yaml:"up,omitempty" json:"up,omitempty"`
	DownMbps int    `yaml:"down,omitempty" json:"down,omitempty"`
	Password string `yaml:"password,omitempty" json:"password,omitempty"`
	OBFS     string `yaml:"obfs,omitempty" json:"obfs,omitempty"`
}

type RealityOpts struct {
	PublicKey string `yaml:"public-key,omitempty" json:"public-key,omitempty"`
	ShortID   string `yaml:"short-id,omitempty" json:"short-id,omitempty"`
}

// ClashConfig 是订阅站按 clash.meta 返回的完整配置。
// 我们只取其中的节点与策略组；端口、DNS 等由本应用统一接管，
// 否则订阅里写死的 127.0.0.1:9090 会和面板抢控制端口。
type ClashConfig struct {
	Proxies     []Proxy      `yaml:"proxies" json:"proxies"`
	ProxyGroups []ClashGroup `yaml:"proxy-groups" json:"proxy-groups"`
	Rules       []string     `yaml:"rules" json:"rules"`
}

// ClashGroup 是订阅里的策略组。
type ClashGroup struct {
	Name     string   `yaml:"name" json:"name"`
	Type     string   `yaml:"type" json:"type"`
	Proxies  []string `yaml:"proxies,omitempty" json:"proxies,omitempty"`
	URL      string   `yaml:"url,omitempty" json:"url,omitempty"`
	Interval int      `yaml:"interval,omitempty" json:"interval,omitempty"`
	Filter   string   `yaml:"filter,omitempty" json:"filter,omitempty"`
	Use      []string `yaml:"use,omitempty" json:"use,omitempty"`
}

// Parsed 是订阅解析结果。
// Clash 非 nil 表示订阅返回的是完整 Clash 配置，节点与规则都取自它。
type Parsed struct {
	Proxies []Proxy
	Groups  []ClashGroup
	Rules   []string
	Clash   bool
}

// ParseSubscription 解析订阅正文，自动识别三种常见形态：
//  1. 完整的 Clash YAML（含 proxies:）—— 节点、策略组与规则全部采用它的
//  2. 整段 base64 的链接列表（机场最常见）
//  3. 明文链接列表
func ParseSubscription(body string) (*Parsed, error) {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return nil, fmt.Errorf("订阅内容为空")
	}

	// 已经是 Clash 配置
	if strings.Contains(trimmed, "proxies:") {
		var cc ClashConfig
		if err := yaml.Unmarshal([]byte(trimmed), &cc); err != nil {
			return nil, fmt.Errorf("Clash 配置解析失败: %w", err)
		}
		if len(cc.Proxies) == 0 {
			return nil, fmt.Errorf("Clash 配置里没有任何节点")
		}
		return &Parsed{
			Proxies: dedupeNames(cc.Proxies),
			Groups:  cc.ProxyGroups,
			Rules:   cc.Rules,
			Clash:   true,
		}, nil
	}

	links := extractLinks(trimmed)
	if len(links) == 0 {
		return nil, fmt.Errorf("未识别到任何节点链接（既不是 base64 链接列表，也不是 Clash 配置）")
	}

	proxies := make([]Proxy, 0, len(links))
	for _, raw := range links {
		p, err := parseLink(raw)
		if err != nil {
			continue
		}
		if p.Name == "" {
			p.Name = fmt.Sprintf("%s:%d", p.Server, p.Port)
		}
		proxies = append(proxies, p)
	}
	if len(proxies) == 0 {
		return nil, fmt.Errorf("解析到 %d 条链接，但没有一条能成功转换", len(links))
	}
	return &Parsed{Proxies: dedupeNames(proxies)}, nil
}

// dedupeNames 处理重名节点：同名加后缀，避免 mihomo 因重名直接拒绝启动。
func dedupeNames(proxies []Proxy) []Proxy {
	used := map[string]int{}
	for i := range proxies {
		name := proxies[i].Name
		if n, ok := used[name]; ok {
			used[name] = n + 1
			proxies[i].Name = fmt.Sprintf("%s-%d", name, n+1)
		} else {
			used[name] = 1
		}
	}
	return proxies
}

// extractLinks 先尝试整体 base64 解码，失败则按明文逐行处理。
func extractLinks(body string) []string {
	candidates := []string{body}
	if decoded, ok := tryBase64(body); ok && strings.Contains(decoded, "://") {
		candidates = []string{decoded}
	}

	var out []string
	for _, text := range candidates {
		for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || !strings.Contains(line, "://") {
				continue
			}
			out = append(out, line)
		}
	}
	return out
}

func tryBase64(s string) (string, bool) {
	clean := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, s)
	if clean == "" {
		return "", false
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(clean); err == nil {
			return string(b), true
		}
	}
	return "", false
}

// MarshalJSON 保留给需要把解析结果存盘的地方。
func (p *Parsed) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{
		"proxies": p.Proxies,
		"groups":  p.Groups,
		"rules":   p.Rules,
		"clash":   p.Clash,
	})
}
