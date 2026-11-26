package fndepot

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"strings"
)

// ReadFpkManifest 读出 .fpk 包里的 manifest。
//
// .fpk 是 gzip 压的 tar，manifest 在根目录。
//
// 为什么要读它：索引里写的应用键、版本、架构必须与包里的 manifest 一致。
// 对不上的后果很具体 —— 键不符，客户端会直接跳过这个应用；
// 版本不符，装上去的版本与源里写的不是一回事，升级判断全乱。
func ReadFpkManifest(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("%s 不是 gzip 压缩的包: %w", path, err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("读 %s 失败: %w", path, err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if strings.TrimPrefix(hdr.Name, "./") != "manifest" {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, 1<<20))
		if err != nil {
			return nil, err
		}
		mf, err := parseManifest(string(b))
		if err != nil {
			return nil, fmt.Errorf("%s 的 manifest 有问题: %w", path, err)
		}
		return mf, nil
	}
	return nil, fmt.Errorf("%s 里没有 manifest", path)
}

// parseManifest 解析 manifest（key = value 的 ini 风格）。
//
// 值里有 = 也不怕：只按第一个 = 切。
func parseManifest(text string) (map[string]string, error) {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.Index(line, "=")
		if i < 0 {
			continue
		}
		out[strings.TrimSpace(line[:i])] = strings.TrimSpace(line[i+1:])
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("manifest 是空的")
	}
	return out, nil
}

// CheckPackage 校验包里的 manifest 与索引条目是否对得上。
func CheckPackage(appKey, version, arch string, mf map[string]string) error {
	var problems []string
	if got := mf["appname"]; got != appKey {
		problems = append(problems, fmt.Sprintf("包里的 appname 是 %q，索引里写的是 %q", got, appKey))
	}
	if got := mf["version"]; got != version {
		problems = append(problems, fmt.Sprintf("包里的 version 是 %q，索引里写的是 %q", got, version))
	}
	if got := mf["platform"]; got != arch {
		problems = append(problems, fmt.Sprintf("包里的 platform 是 %q，索引里写的是 %q", got, arch))
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("包与索引对不上：%s", strings.Join(problems, "；"))
}
