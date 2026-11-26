package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// geoDataFiles 是随应用包分发的分流数据。
//
// 国内 NAS 连不上 GitHub releases，geodata 必须打进包里，
// 装好就能离线按国内/国外分流，不用等内核联网下载。
var geoDataFiles = []string{"geoip.dat", "geoip.metadb", "geosite.dat", "country.mmdb"}

// cmdCopyFiles 把这些数据文件复制到目标目录。
func cmdCopyFiles(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("用法: copyfiles <源目录> <目标目录>")
	}
	return copyGeoData(args[0], args[1])
}

// copyGeoData 把随包分发的分流数据复制到目标目录。
func copyGeoData(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	copied := 0
	for _, name := range geoDataFiles {
		from := filepath.Join(src, name)
		info, err := os.Stat(from)
		if err != nil {
			continue // 缺文件不致命，最后统一检查
		}
		to := filepath.Join(dst, name)
		if err := copyFile(from, to); err != nil {
			return err
		}
		copied++
		fmt.Printf("  已拷贝 %s (%d 字节)\n", name, info.Size())
	}
	if copied == 0 {
		return fmt.Errorf("%s 下没有 geodata 文件，缺少它们内核无法离线分流", src)
	}
	return nil
}

// copyFile 复制文件内容。
func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
