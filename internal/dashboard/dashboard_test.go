package dashboard

import "testing"

func TestClientURL(t *testing.T) {
	cases := []struct {
		name   string
		id     string
		host   string
		port   int
		secret string
		want   string
	}{
		{
			name:   "metacubexd 带密钥",
			id:     "metacubexd",
			host:   "192.168.2.191",
			port:   9090,
			secret: "abc",
			want:   "/ui/#/setup?http=true&hostname=192.168.2.191&port=9090&secret=abc",
		},
		{
			// secret 为空时整个参数都不给。
			// 给一个空的 secret= 会让面板把空串当成真实密钥存下来。
			name: "metacubexd 空密钥",
			id:   "metacubexd",
			host: "192.168.2.191",
			port: 9090,
			want: "/ui/#/setup?http=true&hostname=192.168.2.191&port=9090",
		},
		{
			name:   "yacd 用 query 传参",
			id:     "yacd",
			host:   "192.168.2.191",
			port:   9090,
			secret: "abc",
			want:   "/ui/?hostname=192.168.2.191&port=9090&secret=abc",
		},
		{
			name: "yacd 空密钥",
			id:   "yacd",
			host: "nas.local",
			port: 9090,
			want: "/ui/?hostname=nas.local&port=9090",
		},
		{
			name:   "zashboard",
			id:     "zashboard",
			host:   "10.0.0.5",
			port:   19090,
			secret: "s3cr3t",
			want:   "/ui/#/setup?http=true&hostname=10.0.0.5&port=19090&secret=s3cr3t",
		},
		{
			// IPv6 主机名必须带方括号：面板拿到参数后自己拼地址，
			// 裸 IPv6 会拼出 http://2409::982:9090 这种浏览器解析不了的地址。
			name:   "IPv6 主机名带方括号",
			id:     "metacubexd",
			host:   "2409:8a55:6e9d:2c04::982",
			port:   9090,
			secret: "",
			want:   "/ui/#/setup?http=true&hostname=%5B2409%3A8a55%3A6e9d%3A2c04%3A%3A982%5D&port=9090",
		},
		{
			name:   "zashboard 的 IPv6 主机名",
			id:     "zashboard",
			host:   "2409:8a55:6e9d:2c04::982",
			port:   9090,
			secret: "",
			want:   "/ui/#/setup?http=true&hostname=%5B2409%3A8a55%3A6e9d%3A2c04%3A%3A982%5D&port=9090",
		},
		{
			// yacd 用的是 url.hostname = hostname，走 WHATWG URL。
			// 那里同样要方括号：裸 IPv6 会被 URL 解析器判为非法，
			// 赋值直接被忽略，面板会退回默认地址连不上内核。
			name:   "yacd 的 IPv6 主机名",
			id:     "yacd",
			host:   "2409:8a55:6e9d:2c04::982",
			port:   9090,
			secret: "",
			want:   "/ui/?hostname=%5B2409%3A8a55%3A6e9d%3A2c04%3A%3A982%5D&port=9090",
		},
		{
			// 已经带方括号的就别再套一层，不然会变成 [[...]]。
			name:   "已带方括号的 IPv6",
			id:     "metacubexd",
			host:   "[2409:8a55:6e9d:2c04::982]",
			port:   9090,
			secret: "",
			want:   "/ui/#/setup?http=true&hostname=%5B2409%3A8a55%3A6e9d%3A2c04%3A%3A982%5D&port=9090",
		},
		{
			// IPv4 和域名里没有冒号，不能被当成 IPv6 加括号。
			name:   "IPv4 不加括号",
			id:     "metacubexd",
			host:   "192.168.2.191",
			port:   9090,
			secret: "",
			want:   "/ui/#/setup?http=true&hostname=192.168.2.191&port=9090",
		},
		{
			name:   "域名不加括号",
			id:     "metacubexd",
			host:   "nas.local",
			port:   9090,
			secret: "",
			want:   "/ui/#/setup?http=true&hostname=nas.local&port=9090",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, ok := DashboardByID(tc.id)
			if !ok {
				t.Fatalf("找不到面板: %s", tc.id)
			}
			got := d.ClientURL(tc.host, tc.port, tc.secret)
			if got != tc.want {
				t.Errorf("ClientURL 不匹配: got=%s want=%s", got, tc.want)
			}
		})
	}
}

func TestDashboardURLsSupportedByCore(t *testing.T) {
	// mihomo 的 UI 下载器只认 zip 与 tar.gz，
	// 选一个 xz 的地址内核就解不开，面板会一直是 404。
	for _, d := range Dashboards {
		if !hasSuffix(d.URL, ".zip") && !hasSuffix(d.URL, ".tgz") && !hasSuffix(d.URL, ".tar.gz") {
			t.Errorf("面板 %s 的下载地址格式内核解不开: %s", d.ID, d.URL)
		}
	}
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
