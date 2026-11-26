package sub

import (
	"encoding/base64"
	"testing"
)

func TestParseSubscriptionBase64VLESS(t *testing.T) {
	link := "vless://51f553d9-0e28-488f-809a-548498ac41b7@example.com:443" +
		"?type=ws&encryption=none&host=cdn.example.com&path=%2Fpq%2Fsg1&security=tls&sni=cdn.example.com&fp=ios&insecure=0#节点A"
	body := base64.StdEncoding.EncodeToString([]byte(link + "\n" + link))

	parsed, err := ParseSubscription(body)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if parsed.Clash {
		t.Fatal("不应识别为 Clash YAML")
	}
	if len(parsed.Proxies) != 2 {
		t.Fatalf("期望 2 个节点，实际 %d", len(parsed.Proxies))
	}

	p := parsed.Proxies[0]
	if p.Type != "vless" {
		t.Errorf("类型错误: %s", p.Type)
	}
	if p.Server != "example.com" || p.Port != 443 {
		t.Errorf("地址错误: %s:%d", p.Server, p.Port)
	}
	if p.UUID != "51f553d9-0e28-488f-809a-548498ac41b7" {
		t.Errorf("UUID 错误: %s", p.UUID)
	}
	if !p.TLS {
		t.Error("应启用 TLS")
	}
	if p.Network != "ws" {
		t.Errorf("传输协议错误: %s", p.Network)
	}
	if p.WSOpts == nil || p.WSOpts.Path != "/pq/sg1" {
		t.Errorf("ws 配置错误: %+v", p.WSOpts)
	}
	if p.WSOpts == nil || p.WSOpts.Headers["Host"] != "cdn.example.com" {
		t.Errorf("ws Host 头错误: %+v", p.WSOpts)
	}
	if p.ServerName != "cdn.example.com" {
		t.Errorf("sni 错误: %s", p.ServerName)
	}
	if p.ClientFingerprint != "ios" {
		t.Errorf("指纹错误: %s", p.ClientFingerprint)
	}

	// 同名节点必须被加上后缀，否则 mihomo 会因重名拒绝启动。
	if parsed.Proxies[0].Name == parsed.Proxies[1].Name {
		t.Error("重名节点未自动去重")
	}
}

func TestParseSubscriptionPlainText(t *testing.T) {
	body := "trojan://password@tr.example.com:443?sni=tr.example.com&type=tcp#HK\n" +
		"ss://YWVzLTI1Ni1nY206cGFzcw@ss.example.com:8388#SS"

	parsed, err := ParseSubscription(body)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(parsed.Proxies) != 2 {
		t.Fatalf("期望 2 个节点，实际 %d", len(parsed.Proxies))
	}
	if parsed.Proxies[0].Type != "trojan" || parsed.Proxies[0].Password != "password" {
		t.Errorf("trojan 解析错误: %+v", parsed.Proxies[0])
	}
	if parsed.Proxies[1].Type != "ss" || parsed.Proxies[1].Cipher != "aes-256-gcm" || parsed.Proxies[1].Password != "pass" {
		t.Errorf("ss 解析错误: %+v", parsed.Proxies[1])
	}
}

func TestParseSubscriptionRejectsGarbage(t *testing.T) {
	if _, err := ParseSubscription("这不是订阅内容"); err == nil {
		t.Error("垃圾内容应当报错")
	}
	if _, err := ParseSubscription(""); err == nil {
		t.Error("空内容应当报错")
	}
}
