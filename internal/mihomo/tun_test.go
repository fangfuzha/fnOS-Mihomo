package mihomo

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"fnos-mihomo/internal/settings"
	"fnos-mihomo/internal/sub"
)

// TestGatewayEnablesTun 验证开启本机接管时配置里确实带上了 tun 段。
func TestGatewayEnablesTun(t *testing.T) {
	set := settings.Default()
	set.Gateway.Enabled = true
	parsed := &sub.Parsed{
		Proxies: []sub.Proxy{{Name: "n1", Type: "vless", Server: "a.com", Port: 443, UUID: "u"}},
	}

	cfg, err := Build(*set, parsed)
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}
	if cfg.Tun == nil {
		t.Fatal("开启接管后应生成 tun 配置")
	}
	if !cfg.Tun.Enable {
		t.Error("tun 应为启用状态")
	}
	if cfg.Tun.Device != "Mihomo0" {
		t.Errorf("tun 设备名错误: %s", cfg.Tun.Device)
	}

	b, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	out := string(b)
	if !strings.Contains(out, "tun:") {
		t.Errorf("YAML 里缺少 tun 段:\n%s", firstLines(out, 40))
	}
	if !strings.Contains(out, "enable: true") {
		t.Errorf("tun 未启用:\n%s", firstLines(out, 40))
	}
	if !strings.Contains(out, "device: Mihomo0") {
		t.Errorf("缺少设备名:\n%s", firstLines(out, 40))
	}
}

// TestTunIPv6EnabledByDefault 默认开启 IPv6 代理：
// 内核要接受 IPv6 流量，TUN 也要有自己的 IPv6 地址，
// 否则 IPv6 会绕过代理直连出去。
func TestTunIPv6EnabledByDefault(t *testing.T) {
	set := settings.Default()
	set.Gateway.Enabled = true
	parsed := &sub.Parsed{
		Proxies: []sub.Proxy{{Name: "n1", Type: "vless", Server: "a.com", Port: 443, UUID: "u"}},
	}

	cfg, err := Build(*set, parsed)
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}
	if !cfg.IPv6 {
		t.Error("默认应开启内核的 IPv6 能力")
	}
	if cfg.Tun == nil {
		t.Fatal("开启接管后应生成 tun 配置")
	}
	if len(cfg.Tun.Inet6Address) == 0 {
		t.Error("开启 IPv6 后 TUN 必须有自己的 IPv6 地址")
	}

	b, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	out := string(b)
	if !strings.Contains(out, "inet6-address:") {
		t.Errorf("YAML 里缺少 inet6-address:\n%s", firstLines(out, 40))
	}
	if !strings.Contains(out, "ipv6: true") {
		t.Errorf("YAML 里 ipv6 应为 true:\n%s", firstLines(out, 40))
	}
}

// TestTunIPv6Disabled 关掉 IPv6 时不生成 inet6-address。
func TestTunIPv6Disabled(t *testing.T) {
	set := settings.Default()
	off := false
	set.IPv6 = &off
	set.Gateway.Enabled = true
	parsed := &sub.Parsed{
		Proxies: []sub.Proxy{{Name: "n1", Type: "vless", Server: "a.com", Port: 443, UUID: "u"}},
	}

	cfg, err := Build(*set, parsed)
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}
	if cfg.IPv6 {
		t.Error("关掉后内核不应接受 IPv6 流量")
	}
	if cfg.Tun != nil && len(cfg.Tun.Inet6Address) != 0 {
		t.Errorf("关掉后不该有 inet6-address: %v", cfg.Tun.Inet6Address)
	}
}

// TestGatewayDisabledNoTun 验证关闭接管时不生成 tun 段。
func TestGatewayDisabledNoTun(t *testing.T) {
	set := settings.Default()
	parsed := &sub.Parsed{
		Proxies: []sub.Proxy{{Name: "n1", Type: "vless", Server: "a.com", Port: 443, UUID: "u"}},
	}

	cfg, err := Build(*set, parsed)
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}
	if cfg.Tun != nil {
		t.Error("关闭接管时不应生成 tun 配置")
	}
}
