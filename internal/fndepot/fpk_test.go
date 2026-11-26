package fndepot

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// 造一个最小的 .fpk：gzip 压的 tar，根目录放 manifest。
func writeFpk(t *testing.T, dir string, manifest string) string {
	t.Helper()
	p := filepath.Join(dir, "test.fpk")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "manifest", Mode: 0o644, Size: int64(len(manifest))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(manifest)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadFpkManifest(t *testing.T) {
	dir := t.TempDir()
	p := writeFpk(t, dir, "appname = Mihomo\nversion = 1.0.0\nplatform = x86\n")

	mf, err := ReadFpkManifest(p)
	if err != nil {
		t.Fatalf("读 manifest 失败: %v", err)
	}
	if mf["appname"] != "Mihomo" || mf["version"] != "1.0.0" || mf["platform"] != "x86" {
		t.Fatalf("manifest 内容不对: %v", mf)
	}

	t.Run("不是 gzip 包要报错", func(t *testing.T) {
		bad := filepath.Join(dir, "bad.fpk")
		if err := os.WriteFile(bad, []byte("not gzip"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadFpkManifest(bad); err == nil {
			t.Fatal("应该报错")
		}
	})
}

func TestCheckPackage(t *testing.T) {
	mf := map[string]string{"appname": "Mihomo", "version": "1.0.0", "platform": "x86"}
	if err := CheckPackage("Mihomo", "1.0.0", "x86", mf); err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	for _, c := range []struct {
		key, version, arch string
	}{
		{"别的应用", "1.0.0", "x86"},
		{"Mihomo", "1.1.0", "x86"},
		{"Mihomo", "1.0.0", "arm"},
	} {
		if err := CheckPackage(c.key, c.version, c.arch, mf); err == nil {
			t.Fatalf("应该报错: %+v", c)
		}
	}
}
