package sub

import (
	"encoding/base64"
	"strings"
	"testing"
)

// clashYAMLSample 是机场订阅在 clash.meta User-Agent 下的真实返回格式。
const clashYAMLSample = `mixed-port: 7890
allow-lan: false
mode: rule
external-controller: 127.0.0.1:9090
dns:
    enable: true
    nameserver: [223.5.5.5]
proxies:
    - { name: SG01, type: vless, server: downloadcfpro.example.com, port: 443, uuid: 51f553d9-0e28-488f-809a-548498ac41b7, udp: true, tls: true, skip-cert-verify: false, client-fingerprint: ios, servername: sg1p1.example.com, network: ws, ws-opts: { path: /pq/sg1, headers: { Host: sg1p1.example.com } } }
    - { name: HK02, type: vless, server: hk.example.com, port: 443, uuid: 1111, tls: true, network: grpc, grpc-opts: { grpc-service-name: gsvc } }
proxy-groups:
    - { name: PROXY, type: select, proxies: [SG01, HK02] }
    - { name: AUTO, type: url-test, url: http://www.gstatic.com/generate_204, interval: 300, proxies: [SG01] }
rules:
    - GEOIP,CN,DIRECT
    - MATCH,PROXY
`

func TestParseSubscriptionClashYAML(t *testing.T) {
	parsed, err := ParseSubscription(clashYAMLSample)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !parsed.Clash {
		t.Fatal("应识别为 Clash YAML")
	}
	if len(parsed.Proxies) != 2 {
		t.Fatalf("期望 2 个节点，实际 %d", len(parsed.Proxies))
	}
	if len(parsed.Groups) != 2 {
		t.Fatalf("期望 2 个策略组，实际 %d", len(parsed.Groups))
	}
	if len(parsed.Rules) != 2 {
		t.Fatalf("期望 2 条规则，实际 %d", len(parsed.Rules))
	}

	p := parsed.Proxies[0]
	if p.Name != "SG01" {
		t.Errorf("节点名错误: %s", p.Name)
	}
	if p.Server != "downloadcfpro.example.com" || p.Port != 443 {
		t.Errorf("地址错误: %s:%d", p.Server, p.Port)
	}
	if !p.TLS || p.ServerName != "sg1p1.example.com" {
		t.Errorf("TLS/SNI 错误: %+v", p)
	}
	if p.ClientFingerprint != "ios" {
		t.Errorf("指纹错误: %s", p.ClientFingerprint)
	}
	if p.Network != "ws" || p.WSOpts == nil || p.WSOpts.Path != "/pq/sg1" {
		t.Errorf("ws 配置错误: %+v", p)
	}
	if p.WSOpts == nil || p.WSOpts.Headers["Host"] != "sg1p1.example.com" {
		t.Errorf("ws Host 错误: %+v", p.WSOpts)
	}
	if parsed.Proxies[1].GRPCOpts == nil || parsed.Proxies[1].GRPCOpts.GRPCServiceName != "gsvc" {
		t.Errorf("grpc 配置错误: %+v", parsed.Proxies[1])
	}
	if parsed.Groups[0].Name != "PROXY" || parsed.Groups[0].Type != "select" {
		t.Errorf("策略组错误: %+v", parsed.Groups[0])
	}
	if parsed.Groups[1].Interval != 300 || parsed.Groups[1].URL == "" {
		t.Errorf("url-test 组错误: %+v", parsed.Groups[1])
	}
}

func TestTryBase64Variants(t *testing.T) {
	raw := "hello world"
	for _, s := range []string{
		base64.StdEncoding.EncodeToString([]byte(raw)),
		base64.RawStdEncoding.EncodeToString([]byte(raw)),
	} {
		got, ok := tryBase64(s)
		if !ok || got != raw {
			t.Errorf("base64 变体解析失败: %q -> %q", s, got)
		}
	}
	if _, ok := tryBase64("!!!not base64!!!"); ok {
		t.Error("非法 base64 不应解析成功")
	}
}

func TestParseSubscriptionRealWorldSample(t *testing.T) {
	// 用一条真实形态的机场订阅做回归，防止协议字段改名后悄悄失效。
	link := "vless://uuid-1234@downloadcfpro.example.com:443" +
		"?type=ws&encryption=none&host=sg1p1.example.com&path=%2Fpq%2Fsg1" +
		"&headerType=none&quicSecurity=none&serviceName=&security=tls&fp=ios&insecure=0&pcs=&sni=sg1p1.example.com" +
		"#%E5%89%A9%E4%BD%99%E6%B5%81%E9%87%8F%EF%BC%9A281.02%20GB"
	body := base64.StdEncoding.EncodeToString([]byte(link))

	parsed, err := ParseSubscription(body)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(parsed.Proxies) != 1 {
		t.Fatalf("期望 1 个节点，实际 %d", len(parsed.Proxies))
	}
	if parsed.Proxies[0].Server != "downloadcfpro.example.com" {
		t.Errorf("server 错误: %s", parsed.Proxies[0].Server)
	}
	if !strings.Contains(parsed.Proxies[0].Name, "剩余流量") {
		t.Errorf("节点名未正确解码: %s", parsed.Proxies[0].Name)
	}
}
