package sub

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// parseLink 解析单条节点链接。
func parseLink(raw string) (Proxy, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Proxy{}, err
	}
	q := u.Query()

	name := u.Fragment
	if name == "" {
		name = q.Get("remarks")
	}

	switch strings.ToLower(u.Scheme) {
	case "vless":
		return parseVLESS(u, q, name)
	case "vmess":
		return parseVMess(u, q, name)
	case "trojan":
		return parseTrojan(u, q, name)
	case "ss", "ssr":
		return parseSS(u, q, name)
	case "hysteria2", "hy2":
		return parseHysteria2(u, q, name)
	case "tuic":
		return parseTUIC(u, q, name)
	default:
		return Proxy{}, fmt.Errorf("暂不支持的协议: %s", u.Scheme)
	}
}

func hostPort(u *url.URL) (string, int, error) {
	host := u.Hostname()
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		return "", 0, fmt.Errorf("端口无效: %q", u.Port())
	}
	if host == "" || port == 0 {
		return "", 0, fmt.Errorf("缺少地址或端口")
	}
	return host, port, nil
}

func parseVLESS(u *url.URL, q url.Values, name string) (Proxy, error) {
	host, port, err := hostPort(u)
	if err != nil {
		return Proxy{}, err
	}
	p := Proxy{
		Name: name, Type: "vless", Server: host, Port: port,
		UUID: u.User.Username(), UDP: true,
		Flow: q.Get("flow"),
	}
	if p.UUID == "" {
		return Proxy{}, fmt.Errorf("vless 缺少 uuid")
	}

	security := strings.ToLower(q.Get("security"))
	p.TLS = security == "tls" || security == "reality"
	if security == "reality" {
		p.RealityOpts = &RealityOpts{
			PublicKey: q.Get("pbk"),
			ShortID:   q.Get("sid"),
		}
	}
	p.SkipCertVerify = q.Get("insecure") == "1" || q.Get("allowInsecure") == "1"
	if sni := q.Get("sni"); sni != "" {
		p.ServerName = sni
	} else if h := q.Get("host"); h != "" && !strings.Contains(h, ",") {
		p.ServerName = h
	}
	if fp := q.Get("fp"); fp != "" {
		p.ClientFingerprint = fp
	}
	if alpn := q.Get("alpn"); alpn != "" {
		p.ALPN = strings.Split(alpn, ",")
	}

	network := strings.ToLower(q.Get("type"))
	if network == "" {
		network = "tcp"
	}
	p.Network = network
	switch network {
	case "ws":
		p.WSOpts = &WSOpts{Path: q.Get("path")}
		if h := q.Get("host"); h != "" {
			p.WSOpts.Headers = map[string]string{"Host": h}
		}
	case "grpc":
		p.GRPCOpts = &GRPCOpts{GRPCServiceName: q.Get("serviceName")}
	}
	return p, nil
}

// vmess:// 后跟一段 base64 的 JSON。
func parseVMess(u *url.URL, q url.Values, name string) (Proxy, error) {
	payload := strings.TrimPrefix(u.Opaque, "//")
	if payload == "" {
		payload = u.Host
	}
	decoded, ok := tryBase64(payload)
	if !ok {
		return Proxy{}, fmt.Errorf("vmess 内容不是合法 base64")
	}
	var v struct {
		V    string `json:"v"`
		PS   string `json:"ps"`
		Add  string `json:"add"`
		Port string `json:"port"`
		ID   string `json:"id"`
		AID  string `json:"aid"`
		SCY  string `json:"scy"`
		Net  string `json:"net"`
		Type string `json:"type"`
		Host string `json:"host"`
		Path string `json:"path"`
		TLS  string `json:"tls"`
		SNI  string `json:"sni"`
		ALPN string `json:"alpn"`
	}
	if err := json.Unmarshal([]byte(decoded), &v); err != nil {
		return Proxy{}, fmt.Errorf("vmess JSON 解析失败: %w", err)
	}
	port, _ := strconv.Atoi(v.Port)
	if port == 0 {
		return Proxy{}, fmt.Errorf("vmess 缺少端口")
	}
	if v.PS != "" {
		name = v.PS
	}
	p := Proxy{
		Name: name, Type: "vmess", Server: v.Add, Port: port,
		UUID: v.ID, UDP: true,
	}
	if v.AID != "" {
		p.AlterID, _ = strconv.Atoi(v.AID)
	}
	if v.SCY != "" {
		p.Cipher = v.SCY
	}
	p.TLS = v.TLS == "tls"
	p.ServerName = v.SNI
	if p.ServerName == "" && v.Host != "" && !strings.Contains(v.Host, ",") {
		p.ServerName = v.Host
	}
	if v.ALPN != "" {
		p.ALPN = strings.Split(v.ALPN, ",")
	}
	network := strings.ToLower(v.Net)
	if network == "" {
		network = "tcp"
	}
	p.Network = network
	switch network {
	case "ws":
		p.WSOpts = &WSOpts{Path: v.Path}
		if v.Host != "" {
			p.WSOpts.Headers = map[string]string{"Host": v.Host}
		}
	case "grpc":
		p.GRPCOpts = &GRPCOpts{GRPCServiceName: v.Path}
	}
	return p, nil
}

func parseTrojan(u *url.URL, q url.Values, name string) (Proxy, error) {
	host, port, err := hostPort(u)
	if err != nil {
		return Proxy{}, err
	}
	password := u.User.Username()
	if p, ok := u.User.Password(); ok {
		password = password + ":" + p
	}
	if password == "" {
		return Proxy{}, fmt.Errorf("trojan 缺少密码")
	}
	p := Proxy{
		Name: name, Type: "trojan", Server: host, Port: port,
		Password: password, TLS: true, UDP: true,
	}
	if sni := q.Get("sni"); sni != "" {
		p.ServerName = sni
	} else if h := q.Get("host"); h != "" && !strings.Contains(h, ",") {
		p.ServerName = h
	}
	p.SkipCertVerify = q.Get("allowInsecure") == "1" || q.Get("insecure") == "1"
	if alpn := q.Get("alpn"); alpn != "" {
		p.ALPN = strings.Split(alpn, ",")
	}
	network := strings.ToLower(q.Get("type"))
	if network == "" {
		network = "tcp"
	}
	p.Network = network
	switch network {
	case "ws":
		p.WSOpts = &WSOpts{Path: q.Get("path")}
		if h := q.Get("host"); h != "" {
			p.WSOpts.Headers = map[string]string{"Host": h}
		}
	case "grpc":
		p.GRPCOpts = &GRPCOpts{GRPCServiceName: q.Get("serviceName")}
	}
	return p, nil
}

func parseSS(u *url.URL, q url.Values, name string) (Proxy, error) {
	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	if host == "" || port == 0 {
		return Proxy{}, fmt.Errorf("ss 缺少地址或端口")
	}

	// 两种常见写法：ss://base64(method:pass)@host:port 与 ss://base64(method:pass@host:port)
	raw := u.User.Username()
	if p, ok := u.User.Password(); ok {
		raw += ":" + p
	}
	if decoded, ok := tryBase64(raw); ok && strings.Contains(decoded, ":") {
		raw = decoded
	} else if decoded, ok := tryBase64(u.Opaque); ok && strings.Contains(decoded, "@") {
		// base64(method:pass@host:port) 形式：地址要从解码结果里取
		at := strings.LastIndex(decoded, "@")
		raw = decoded[:at]
		hostPort := decoded[at+1:]
		if hp, err := url.Parse("ss://" + hostPort); err == nil {
			host = hp.Hostname()
			if v, err := strconv.Atoi(hp.Port()); err == nil {
				port = v
			}
		}
	}

	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		return Proxy{}, fmt.Errorf("ss 无法解析加密方式与密码")
	}
	return Proxy{
		Name: name, Type: "ss", Server: host, Port: port,
		Cipher: parts[0], Password: parts[1], UDP: true,
	}, nil
}

func parseHysteria2(u *url.URL, q url.Values, name string) (Proxy, error) {
	host, port, err := hostPort(u)
	if err != nil {
		return Proxy{}, err
	}
	password := u.User.Username()
	if p, ok := u.User.Password(); ok && password != "" {
		password = password + ":" + p
	}
	p := Proxy{
		Name: name, Type: "hysteria2", Server: host, Port: port,
		TLS: true, UDP: true,
		Hysteria2Opts: &Hysteria2Opts{Password: password},
	}
	if sni := q.Get("sni"); sni != "" {
		p.ServerName = sni
	}
	p.SkipCertVerify = q.Get("insecure") == "1"
	if obfs := q.Get("obfs"); obfs != "" {
		p.Hysteria2Opts.OBFS = obfs
	}
	if alpn := q.Get("alpn"); alpn != "" {
		p.ALPN = strings.Split(alpn, ",")
	}
	return p, nil
}

func parseTUIC(u *url.URL, q url.Values, name string) (Proxy, error) {
	host, port, err := hostPort(u)
	if err != nil {
		return Proxy{}, err
	}
	p := Proxy{
		Name: name, Type: "tuic", Server: host, Port: port,
		TLS: true, UDP: true,
	}
	p.UUID = u.User.Username()
	if pw, ok := u.User.Password(); ok {
		p.Password = pw
	}
	if sni := q.Get("sni"); sni != "" {
		p.ServerName = sni
	}
	p.SkipCertVerify = q.Get("allow_insecure") == "1" || q.Get("insecure") == "1"
	return p, nil
}
