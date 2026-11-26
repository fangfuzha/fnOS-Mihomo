package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// defaultMihomoVersion 是打包时默认使用的内核版本。
// 需要换版本时设环境变量 MIHOMO_VERSION 即可。
const defaultMihomoVersion = "v1.19.34"

// cmdFetchMihomo 下载 mihomo 内核（双架构）到 assets/。
//
// 内核在 Release 里是 .gz 压缩的二进制，打包时再由 ungz 解开。
// 文件已存在就跳过，方便反复执行。
func cmdFetchMihomo(args []string) error {
	version := os.Getenv("MIHOMO_VERSION")
	if version == "" {
		version = defaultMihomoVersion
	}
	dir := "assets"
	if len(args) > 0 {
		dir = args[0]
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	base := "https://github.com/MetaCubeX/mihomo/releases/download/" + version
	for _, arch := range []string{"amd64", "arm64"} {
		out := filepath.Join(dir, "mihomo-linux-"+arch+".gz")
		if _, err := os.Stat(out); err == nil {
			fmt.Printf("已存在，跳过: %s\n", out)
			continue
		}
		url := fmt.Sprintf("%s/mihomo-linux-%s-%s.gz", base, arch, version)
		fmt.Printf("==> 下载 mihomo %s linux-%s\n", version, arch)
		fmt.Printf("    %s\n", url)
		if err := download(url, out); err != nil {
			return err
		}
	}
	fmt.Println("==> 完成")
	return nil
}

// download 下载文件并重试几次。
// 先写临时文件再改名，中断时不会留下半个损坏的压缩包。
func download(url, out string) error {
	const retries = 3
	client := &http.Client{Timeout: 10 * time.Minute}
	var lastErr error
	for i := 0; i < retries; i++ {
		if i > 0 {
			fmt.Printf("  重试 %d/%d: %v\n", i+1, retries, lastErr)
			time.Sleep(3 * time.Second)
		}
		if err := downloadOnce(client, url, out); err != nil {
			lastErr = err
			continue
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

func downloadOnce(client *http.Client, url, out string) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	tmp := out + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	// 校验下载到的是不是 gzip。GitHub 出错时会返回一个 HTML 页面，
	// 直接改名成 .gz 会让打包时的解压步骤莫名失败，在这里早点报错更清楚。
	// 读取放在独立函数里：Windows 上文件句柄没关就改名会失败。
	head, err := readHead(tmp, 2)
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("下载内容不完整: %w", err)
	}
	if head[0] != 0x1f || head[1] != 0x8b {
		os.Remove(tmp)
		return fmt.Errorf("下载到的不是 gzip 文件")
	}
	return os.Rename(tmp, out)
}

// readHead 读取文件开头的 n 个字节，读完立即关闭句柄。
func readHead(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
