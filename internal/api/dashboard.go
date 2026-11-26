package api

import (
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"fnos-mihomo/internal/dashboard"
)

// handleDashboard 返回第三方面板的信息。
//
// host 取自请求的 Host 头，这样面板里生成的链接用的就是
// 用户实际访问 NAS 的那个地址（可能是 IP、域名或 IPv6），
// 而不是 127.0.0.1 —— 否则用户在自己电脑上打开面板，
// 面板会去连自己电脑上的 9090 端口，必然连不上。
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	set := s.store.Get()

	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")

	options := make([]map[string]string, 0, len(dashboard.Dashboards))
	for _, d := range dashboard.Dashboards {
		options = append(options, map[string]string{
			"id":   d.ID,
			"name": d.Name,
		})
	}

	resp := map[string]any{
		"current": set.Dashboard,
		"options": options,
	}

	// 面板下载状态：下载中或失败时告诉界面，别让人对着 404 发呆。
	if s.DashboardStatus != nil {
		status, errMsg := s.DashboardStatus()
		resp["status"] = status
		if errMsg != "" {
			resp["error"] = errMsg
		}
	}

	resp["controllerPort"] = set.ControllerPort

	if d, ok := dashboard.DashboardByID(set.Dashboard); ok {
		resp["url"] = dashboardURL(d, host, set.ControllerPort, set.ControllerSecret)
		resp["name"] = d.Name

		// IPv6 快捷入口：把本机的全球单播 IPv6 也列出来。
		// 用户从 IPv4 地址进来的话，一键就能切到 IPv6，
		// 不用自己拼那个带方括号的长地址。
		v6 := make([]map[string]string, 0, 2)
		for _, addr := range globalIPv6() {
			v6 = append(v6, map[string]string{
				"addr": addr,
				"url":  dashboardURL(d, addr, set.ControllerPort, set.ControllerSecret),
			})
		}
		resp["ipv6"] = v6
	}
	writeJSON(w, http.StatusOK, resp)
}

// dashboardURL 生成某个主机地址下的第三方面板地址。
//
// 面板由内核在控制端口的 /ui/ 下托管，所以地址要带上控制端口。
// 不能只给相对路径：相对路径会落到面板服务自己的端口上，
// 而不是内核的控制端口。
//
// IPv6 主机名的方括号由两处各管一半：
// JoinHostPort 给基础地址补，ClientURL 给 hostname 参数补。
func dashboardURL(d dashboard.Dashboard, host string, port int, secret string) string {
	base := "http://" + net.JoinHostPort(host, strconv.Itoa(port))
	return base + d.ClientURL(host, port, secret)
}

// globalIPv6 列出本机的全球单播 IPv6 地址。
func globalIPv6() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	return filterGlobalIPv6(addrs)
}

// filterGlobalIPv6 从网卡地址里挑出全球单播的 IPv6，并按地址排序。
//
// 只取 2000::/3 这一段：
//   - 链路本地地址（fe80::/10）浏览器得带网卡后缀才能用，不实用；
//   - 唯一本地地址（fc00::/7）里就有内核接管用的 TUN 地址
//     （fdfe:dcba:9876::1），那只存在于本机的虚拟网卡上，浏览器连不上。
//
// 排序是为了多次调用顺序稳定，界面上的列表不会跳来跳去。
func filterGlobalIPv6(addrs []net.Addr) []string {
	out := make([]string, 0, 2)
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ipnet.IP.To4() != nil {
			continue // IPv4 不算
		}
		v6 := ipnet.IP.To16()
		if v6 == nil || v6.IsLoopback() {
			continue
		}
		if v6[0]&0xE0 != 0x20 {
			continue // 不在 2000::/3，不是全球单播
		}
		out = append(out, v6.String())
	}
	sort.Strings(out)
	return out
}
