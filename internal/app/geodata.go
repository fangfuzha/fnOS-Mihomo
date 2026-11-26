package app

import (
	"io"
	"os"
	"path/filepath"
)

// geoDataFiles 是内核会去找的分流数据文件。
var geoDataFiles = []string{"geoip.dat", "geoip.metadb", "geosite.dat", "country.mmdb"}

// CopyGeoData 把 geodata 复制到内核的工作目录。
//
// 内核既认配置里写明的路径，也会在工作目录里找 —— 两处都放一份当双保险。
// 已经存在的文件不动：用户可能自己换过。
func CopyGeoData(geoDir string, dataDir string) {
	for _, name := range geoDataFiles {
		src := filepath.Join(geoDir, name)
		dst := filepath.Join(dataDir, name)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if _, err := os.Stat(dst); os.IsNotExist(err) {
			_ = copyFile(src, dst)
		}
	}
}

// copyFile 复制文件内容。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
