package main

import (
	"fmt"
	"os"
	"path/filepath"

	"fnos-mihomo/internal/dashboard"
)

// cmdCopyDashboard 把第三方面板归档复制到目标目录。
//
// 归档是可选的：没跑过 fetch_dashboard 时 assets/ 里没有这些文件，
// 这时跳过即可 —— 运行时面板会自己从 GitHub 拉。
// 想让 NAS 装好就能离线用面板，就先跑一次 fetch_dashboard。
func cmdCopyDashboard(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("用法: copy_dashboard <assets目录> <目标目录>")
	}
	return copyDashboardArchives(args[0], args[1])
}

// copyDashboardArchives 把第三方面板归档与版本清单复制到目标目录。
func copyDashboardArchives(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}

	copied := 0
	for _, d := range dashboard.Dashboards {
		from := filepath.Join(src, d.Archive)
		info, err := os.Stat(from)
		if err != nil {
			continue // 没下载过就跳过，不算失败
		}
		if err := copyFile(from, filepath.Join(dst, d.Archive)); err != nil {
			return err
		}
		copied++
		fmt.Printf("  已拷贝 %s (%d 字节)\n", d.Archive, info.Size())
	}

	// 清单要一起带上：里面记着每个归档对应的版本号，
	// 运行时靠它判断本地这份是不是最新的。
	manifest := filepath.Join(src, dashboard.ManifestName)
	if info, err := os.Stat(manifest); err == nil {
		if err := copyFile(manifest, filepath.Join(dst, dashboard.ManifestName)); err != nil {
			return err
		}
		fmt.Printf("  已拷贝 %s (%d 字节)\n", dashboard.ManifestName, info.Size())
	}

	if copied == 0 {
		fmt.Println("  没有面板归档，跳过（运行时按需从 GitHub 下载）")
	}
	return nil
}
