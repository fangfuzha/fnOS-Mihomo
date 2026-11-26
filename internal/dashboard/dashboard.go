package dashboard

import (
	"net/url"
	"strconv"
	"strings"
)

// Dashboard 描述一个可以在线拉取的第三方面板。
//
// mihomo 的 UI 下载器（component/updater/update_ui.go 的 detectFileType）
// 只认两种格式：zip 与 tar.gz。
//   - MetaCubeXD 的 release 资产是 compressed-dist.tgz（tar.gz），直接可用。
//   - zashboard 的 dist.zip 可用，解压后是单根目录 dist/，
//     mihomo 的 moveDir 会自动剥掉这一层目录。
//   - yacd 官方 release 只有 yacd.tar.xz，解不开（xz 不在支持列表里），
//     所以改用 MetaCubeX 维护的 gh-pages 分支快照：
//     GitHub 的 archive 端点输出 zip，同样是单根目录，moveDir 能处理。
//
// 为什么不让内核去拉 CDN 上的在线站点（Clash Verge 用的那三个地址）：
// 浏览器会拦截。https 页面发起的 http://192.168.x.x:9090 请求属于
// mixed content，实测直接报 NetworkError，内核控制口根本连不上。
// 面板下到本地、由内核自己在 :9090/ui/ 提供就没有这个问题。
type Dashboard struct {
	// ID 是面板标识，也是配置里保存的值。
	ID string
	// Name 是显示名称。
	Name string
	// URL 是 external-ui-url 的下载地址。
	URL string
	// ClientPath 是浏览器里打开面板的路径，形如
	// "/ui/#/setup?http=true&hostname={host}&port={port}&secret={secret}"。
	// 这里用的是真实的参数名（hostname / port / secret），
	// 而不是位置占位符 —— 参数名才是面板认的。
	// secret 为空时该参数整体省略。
	ClientPath string
	// Archive 是本地归档的文件名。
	//
	// 面板归档和 mihomo 内核、geodata 一样是「随包分发 + 本地留一份」的资源：
	// 开发机放在 assets/，随包那份放在应用目录里，运行时下载的新版本放进数据目录。
	// 有本地副本且版本是最新的就不去 GitHub 拉，省流量也省得看 GitHub 不通时的超时。
	Archive string
	// VersionURL 是探测上游最新版本的地址。
	VersionURL string
	// VersionKind 决定怎么从 VersionURL 的响应里取出版本号。
	VersionKind VersionKind
}

// VersionKind 是上游版本号的取法。
type VersionKind int

const (
	// VersionFromRedirect 从 302 重定向的 Location 里取版本号。
	//
	// GitHub 的 releases/latest 会 302 到 /releases/tag/<tag>，
	// 那个 tag 就是最新的 release 版本。
	// 走重定向而不是 api.github.com：后者按出口 IP 限流，
	// 同一台 NAS 上多个面板一起探测很容易撞上 403。
	VersionFromRedirect VersionKind = iota

	// VersionFromAtom 从 commits 的 atom 订阅里取最新提交的 SHA。
	//
	// yacd 用的是 gh-pages 分支快照，没有 release 版本号，
	// 分支的最新提交就是它的「版本」。
	VersionFromAtom
)

// Dashboards 是可选的面板列表。
var Dashboards = []Dashboard{
	{
		ID:   "metacubexd",
		Name: "MetaCubeXD",
		URL:  "https://github.com/MetaCubeX/metacubexd/releases/latest/download/compressed-dist.tgz",
		// MetaCubeXD 用 hash 路由传参，与 Clash Verge 的写法一致。
		ClientPath:  "/ui/#/setup?http=true&hostname={host}&port={port}&secret={secret}",
		Archive:     "metacubexd.tgz",
		VersionURL:  "https://github.com/MetaCubeX/metacubexd/releases/latest",
		VersionKind: VersionFromRedirect,
	},
	{
		ID:          "zashboard",
		Name:        "zashboard",
		URL:         "https://github.com/Zephyruso/zashboard/releases/latest/download/dist.zip",
		ClientPath:  "/ui/#/setup?http=true&hostname={host}&port={port}&secret={secret}",
		Archive:     "zashboard.zip",
		VersionURL:  "https://github.com/Zephyruso/zashboard/releases/latest",
		VersionKind: VersionFromRedirect,
	},
	{
		ID:   "yacd",
		Name: "Yacd",
		URL:  "https://github.com/MetaCubeX/yacd/archive/refs/heads/gh-pages.zip",
		// yacd 用 query 传参，不走 hash 路由。
		ClientPath:  "/ui/?hostname={host}&port={port}&secret={secret}",
		Archive:     "yacd.zip",
		VersionURL:  "https://github.com/MetaCubeX/yacd/commits/gh-pages.atom",
		VersionKind: VersionFromAtom,
	},
}

// DashboardByID 按标识查找面板。
func DashboardByID(id string) (Dashboard, bool) {
	for _, d := range Dashboards {
		if d.ID == id {
			return d, true
		}
	}
	return Dashboard{}, false
}

// ClientURL 生成浏览器里打开面板的完整地址。
//
// host 必须是浏览器访问 NAS 用的那个地址（不是 127.0.0.1），
// 否则面板会去连访问者自己电脑上的控制口。
// host 是 IPv6 地址时不用带方括号，这里会自己补上（原因见 bracketIPv6）。
// secret 是可选的：留空时不带 secret 参数，面板会按无认证方式连接。
func (d Dashboard) ClientURL(host string, port int, secret string) string {
	// ClientPath 里 ? 一定在 # 之后（hash 路由的参数），
	// 所以按第一个 ? 切分即可，前缀部分原样保留。
	prefix, query, _ := strings.Cut(d.ClientPath, "?")

	params := []string{}
	for _, kv := range strings.Split(query, "&") {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch key {
		case "hostname":
			params = append(params, "hostname="+url.QueryEscape(bracketIPv6(host)))
		case "port":
			params = append(params, "port="+strconv.Itoa(port))
		case "secret":
			// secret 是可选的：为空就整个参数都不给。
			// 给一个空的 secret= 会让面板把空串当成真实密钥存下来。
			if secret != "" {
				params = append(params, "secret="+url.QueryEscape(secret))
			}
		default:
			params = append(params, kv)
		}
	}

	out := prefix
	if len(params) > 0 {
		out += "?" + strings.Join(params, "&")
	}
	return out
}

// bracketIPv6 给 IPv6 字面量补上中括号。
//
// 三个面板拿到 hostname 参数后都是自己拼地址的，
// 但拼法不一样，对 IPv6 的要求也不一样：
//   - MetaCubeXD：${protocol}//${hostname}:${port}   裸拼接
//   - zashboard ：${protocol}://${host}:${port}       裸拼接
//   - yacd      ：url.hostname = hostname             走 WHATWG URL
//
// 前两个自己拼，裸 IPv6 会拼出 http://2409::982:9090 这种浏览器解析不了的地址；
// yacd 看着更安全，实际也不行：URL 的主机名解析器遇到冒号会判定非法，
// 这次赋值被直接忽略，面板会退回默认地址，照样连不上内核。
// 所以三种写法都得让 hostname 自带方括号。
//
// 域名和 IPv4 里没有冒号，不用动。
func bracketIPv6(host string) string {
	h := strings.TrimSpace(host)
	if h == "" {
		return h
	}
	// 已经带括号的就别再套一层，不然会变成 [[...]]
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		return h
	}
	// 只有 IPv6 字面量里才有冒号
	if !strings.Contains(h, ":") {
		return h
	}
	return "[" + h + "]"
}
