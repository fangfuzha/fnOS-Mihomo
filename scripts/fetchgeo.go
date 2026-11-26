package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// geodataFiles 是随包分发的分流数据。
//
// 全部来自 MetaCubeX/meta-rules-dat —— mihomo 自己自动更新 geodata 时
// 默认也是这个源。国内 NAS 连不上 GitHub releases，所以要随包分发。
//
// 想固定版本就设 GEODATA_VERSION（tag），默认取最新版。
var geodataFiles = []struct {
	name string
	file string
}{
	{"geoip.dat", "geoip.dat"},
	{"geosite.dat", "geosite.dat"},
	{"geoip.metadb", "geoip.metadb"},
	{"country.mmdb", "country.mmdb"},
}

// geodataBase 是 geodata 的下载源。
const geodataBase = "https://github.com/MetaCubeX/meta-rules-dat/releases"

// cmdFetchGeodata 下载 geodata 到 assets/geodata/。
//
// 语义和 fetch_mihomo 一致：文件已存在就跳过，方便反复执行。
func cmdFetchGeodata(args []string) error {
	dir := filepath.Join("assets", "geodata")
	if len(args) > 0 {
		dir = args[0]
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	version := os.Getenv("GEODATA_VERSION")
	for _, f := range geodataFiles {
		out := filepath.Join(dir, f.name)
		if _, err := os.Stat(out); err == nil {
			fmt.Printf("已存在，跳过: %s\n", out)
			continue
		}
		var url string
		if version != "" {
			url = fmt.Sprintf("%s/download/%s/%s", geodataBase, version, f.file)
		} else {
			url = fmt.Sprintf("%s/latest/download/%s", geodataBase, f.file)
		}
		fmt.Printf("==> 下载 %s\n", f.name)
		if err := downloadFile(url, out); err != nil {
			return fmt.Errorf("下载 %s 失败: %w", f.name, err)
		}
	}
	fmt.Println("==> 完成")
	return nil
}

// downloadFile 下载一个普通文件。
//
// fetchmihomo.go 里的 download 会检查 gzip 头 —— 那是给内核压缩包用的；
// geodata 是裸的 .dat / .mmdb，不能用那个检查。
//
// 先写 .part 再改名：中断时不会留下半个损坏的文件，
// 而半个文件看着「存在」，下次执行就会被跳过。
func downloadFile(url, out string) error {
	const retries = 3
	client := &http.Client{Timeout: 15 * time.Minute}
	tmp := out + ".part"
	var lastErr error

	for i := 0; i < retries; i++ {
		if i > 0 {
			fmt.Printf("  重试 %d/%d: %v\n", i+1, retries, lastErr)
			time.Sleep(3 * time.Second)
		}
		if err := fetchFileOnce(client, url, tmp); err != nil {
			lastErr = err
			os.Remove(tmp)
			continue
		}
		if err := os.Rename(tmp, out); err != nil {
			os.Remove(tmp)
			return err
		}
		info, err := os.Stat(out)
		if err != nil {
			return err
		}
		fmt.Printf("  完成 %s (%d 字节)\n", out, info.Size())
		return nil
	}
	return fmt.Errorf("下载 %s 失败: %w", url, lastErr)
}

// fetchFileOnce 下载一次到临时文件。
func fetchFileOnce(client *http.Client, url, dst string) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, resp.Body)
	cerr := f.Close()
	if err != nil {
		return err
	}
	if cerr != nil {
		return cerr
	}
	// 太小一定是出错了：GitHub 出错时会返回一个 HTML 页面或空文件。
	if n < 100*1024 {
		return fmt.Errorf("下载到的文件只有 %d 字节，不像是 %s", n, dst)
	}
	return nil
}
