package main

import (
	"fmt"
	"time"

	"fnos-mihomo/internal/dashboard"
)

// cmdFetchDashboard 下载或更新第三方面板归档到 assets/。
//
// 语义和 fetch_mihomo 对齐，只是多了一层版本判断：
//   - 本地有，且就是上游最新版 → 复用，不下载；
//   - 本地没有，或上游出了新版本 → 重新下载。
//
// 归档随后会被打进应用包。NAS 上装好就有本地那份可用，
// 不用每次都去 GitHub 拉（国内 NAS 直连 GitHub 常常失败）。
func cmdFetchDashboard(args []string) error {
	dir := "assets"
	if len(args) > 0 {
		dir = args[0]
	}
	cache := &dashboard.Cache{Dir: dir}
	opts := dashboard.DownloadOptions{
		Attempts: 3,
		Timeout:  10 * time.Minute,
		// 脚本要的是确定的上游状态：每次都探测。
		// CheckInterval 留 0 就是这个意思。
	}
	for _, d := range dashboard.Dashboards {
		fmt.Printf("==> %s\n", d.Name)
		a, err := cache.Ensure(d, opts)
		if err != nil {
			return fmt.Errorf("面板 %s 处理失败: %w", d.Name, err)
		}
		switch {
		case a.Fetched:
			fmt.Printf("    已下载 %s (%s)\n", a.Path, a.Version)
		case a.Stale:
			fmt.Printf("    上游探测失败，沿用本地副本 %s (%s)\n", a.Path, a.Version)
		default:
			fmt.Printf("    已是最新，复用 %s (%s)\n", a.Path, a.Version)
		}
	}
	fmt.Println("==> 完成")
	return nil
}
