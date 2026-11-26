package dashboard

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"fnos-mihomo/internal/safeio"
)

// DownloadOptions 控制面板下载的传输方式。
type DownloadOptions struct {
	// ProxyURL 是可选的本地代理地址（如 http://127.0.0.1:7890）。
	// 国内直连 GitHub 常常被墙，走本地代理反而更稳。
	ProxyURL string
	// Attempts 是每条路径的重试次数。
	Attempts int
	// Timeout 是单次下载的整体超时。
	Timeout time.Duration
	// CheckInterval 是两次「探测上游版本」之间的最小间隔。
	//
	// 为 0 表示每次都探测（打包脚本就该这样：它要的是确定的上游状态）。
	// 运行时用它避免每次设置变化都去问一遍 GitHub。
	CheckInterval time.Duration
}

// buildClients 造下载用的客户端列表：先走本地代理，再直连。
func buildClients(opts DownloadOptions) []*http.Client {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	var clients []*http.Client
	if opts.ProxyURL != "" {
		if u, err := url.Parse(opts.ProxyURL); err == nil {
			clients = append(clients, newUIClient(u, timeout))
		}
	}
	clients = append(clients, newUIClient(nil, timeout))
	return clients
}

// newUIClient 造一个下载用的 HTTP 客户端。
//
// 超时给得比默认值宽：GitHub 走节点时实测要 15~30 秒，
// 默认的 30 秒拨号超时经常刚好卡在失败线上。
func newUIClient(proxy *url.URL, timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 60 * time.Second, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		DialContext:         dialer.DialContext,
		TLSHandshakeTimeout: 60 * time.Second,
		IdleConnTimeout:     90 * time.Second,
	}
	if proxy != nil {
		tr.Proxy = http.ProxyURL(proxy)
	} else {
		// 没指定代理时认环境变量里的 HTTPS_PROXY：
		// 开发机上跑维护脚本拉面板归档时经常需要走代理。
		tr.Proxy = http.ProxyFromEnvironment
	}
	return &http.Client{Transport: tr, Timeout: timeout}
}

// InstallArchive 把本地归档解压安装到 destDir。
//
// 归档可以来自随包分发的那份，也可以是运行时缓存里的新版本。
func InstallArchive(archivePath string, destDir string) error {
	data, err := os.ReadFile(archivePath)
	if err != nil {
		return err
	}
	return installUI(data, destDir)
}

// installUI 解压面板并装到 destDir。
func installUI(data []byte, destDir string) error {
	// 先解压到临时目录，成功后再替换正式目录。
	// 直接解压到正式目录的话，中途失败会留下半个面板。
	parent := filepath.Dir(destDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, ".ui-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	if err := extractUI(data, tmp); err != nil {
		return fmt.Errorf("解压面板失败: %w", err)
	}

	// 面板压缩包里通常只有一层根目录（dist/ 之类），
	// 直接把内容摊到目标目录下面。
	src := tmp
	if entries, err := os.ReadDir(tmp); err == nil && len(entries) == 1 && entries[0].IsDir() {
		src = filepath.Join(tmp, entries[0].Name())
	}

	// 换成正式目录：先删掉旧的，再把临时目录改名过去。
	// 同一文件系统内改名是原子的，中途失败也不会留下半份面板。
	if err := os.RemoveAll(destDir); err != nil {
		return err
	}
	if err := safeio.Rename(src, destDir); err != nil {
		// 跨文件系统时改名会失败，退回复制。
		if err := copyDir(src, destDir); err != nil {
			return err
		}
	}
	return nil
}

// extractUI 按文件头判断压缩格式并解压。
//
// mihomo 自己的下载器也只支持 zip 与 tar.gz，
// 所以面板的下载地址必须选这两种格式的。
func extractUI(data []byte, dest string) error {
	if len(data) < 4 {
		return fmt.Errorf("数据太短，不是有效的压缩包")
	}
	switch {
	case data[0] == 'P' && data[1] == 'K':
		return unzipUI(data, dest)
	case data[0] == 0x1f && data[1] == 0x8b:
		return untgzUI(data, dest)
	default:
		return fmt.Errorf("不支持的压缩格式（只支持 zip 和 tar.gz）")
	}
}

func unzipUI(data []byte, dest string) error {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	for _, f := range r.File {
		// 防止压缩包里的路径逃逸出目标目录。
		target := filepath.Join(dest, filepath.Clean("/"+f.Name))
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(target)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func untgzUI(data []byte, dest string) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer gz.Close()
	tarReader := tar.NewReader(gz)
	for {
		hdr, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dest, filepath.Clean("/"+hdr.Name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.Create(target)
			if err != nil {
				return err
			}
			// 解出来的文件权限取原始值，但要保证至少有执行位之外的读权限。
			mode := hdr.FileInfo().Mode().Perm()
			if mode == 0 {
				mode = 0o644
			}
			if _, err := io.Copy(out, tarReader); err != nil {
				out.Close()
				return err
			}
			out.Close()
			if err := os.Chmod(target, mode); err != nil {
				return err
			}
		}
	}
	return nil
}

// copyDir 把 src 下的内容复制到 dst，dst 已有的同名文件会被覆盖。
func copyDir(src string, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := os.MkdirAll(d, 0o755); err != nil {
				return err
			}
			if err := copyDir(s, d); err != nil {
				return err
			}
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if err := copyFile(s, d, info.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src string, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
