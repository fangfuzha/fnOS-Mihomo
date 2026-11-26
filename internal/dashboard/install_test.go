package dashboard

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeZip 造一个只有 index.html 的 zip 包。
func makeZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("<html>panel</html>")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// makeTgz 造一个只有 index.html 的 tar.gz 包。
func makeTgz(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("<html>panel</html>")
	if err := tw.WriteHeader(&tar.Header{
		Name:     "index.html",
		Mode:     0o644,
		Size:     int64(len(body)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractUI(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{"zip", makeZip(t)},
		{"tar.gz", makeTgz(t)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dest := t.TempDir()
			if err := extractUI(tc.data, dest); err != nil {
				t.Fatalf("解压失败: %v", err)
			}
			b, err := os.ReadFile(filepath.Join(dest, "index.html"))
			if err != nil {
				t.Fatalf("没解出 index.html: %v", err)
			}
			if !strings.Contains(string(b), "panel") {
				t.Errorf("index.html 内容不对: %s", b)
			}
		})
	}
}

// TestExtractUIRejectsUnknownFormat 不认识的压缩格式要报错，
// 而不是解出一个空目录。
func TestExtractUIRejectsUnknownFormat(t *testing.T) {
	dest := t.TempDir()
	if err := extractUI([]byte("not an archive at all"), dest); err == nil {
		t.Fatal("期望报错，实际没有")
	}
}

// TestExtractUIBlocksPathEscape 压缩包里的 ../ 不能写到目标目录外面。
func TestExtractUIBlocksPathEscape(t *testing.T) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create("../escaped.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("bad")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	base := t.TempDir()
	dest := filepath.Join(base, "ui")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := extractUI(buf.Bytes(), dest); err != nil {
		t.Fatalf("解压失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "escaped.txt")); err == nil {
		t.Fatal("文件逃出了目标目录")
	}
}

// TestCopyDirOverwrites 覆盖已有文件时不能报错。
func TestCopyDirOverwrites(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "a.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyDir(src, dst); err != nil {
		t.Fatalf("复制失败: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dst, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "new" {
		t.Errorf("没有覆盖旧文件: %s", b)
	}
}
