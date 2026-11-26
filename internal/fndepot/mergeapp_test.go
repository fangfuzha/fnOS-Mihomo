package fndepot

import (
	"strings"
	"testing"
)

// 合并应用条目时，应用级字段要取新的 ——
// 之前沿用旧条目，导致改了 readme_url 也推不出去。
func TestMergeApp(t *testing.T) {
	t.Run("应用级字段取新的，老版本留着", func(t *testing.T) {
		old := validApp()
		old.ReadmeURL = "https://github.com/o/r/blob/main/README.md"

		fresh := validApp()
		fresh.ReadmeURL = "https://raw.githubusercontent.com/o/r/main/README.md"
		fresh.Releases = map[string]Release{
			"1.1.0": {
				Changelog: "第二个版本",
				Packages: map[string]Package{
					"x86": {DownloadURL: "https://example.com/Mihomo-1.1.0-x86.fpk", SHA256: strings.Repeat("e", 64), Size: 300},
				},
			},
		}

		merged, err := MergeApp(old, fresh)
		if err != nil {
			t.Fatalf("合并失败: %v", err)
		}
		if merged.ReadmeURL != fresh.ReadmeURL {
			t.Fatalf("readme_url 没更新: %q", merged.ReadmeURL)
		}
		if len(merged.Releases) != 2 {
			t.Fatalf("版本数不对: %d", len(merged.Releases))
		}
		if _, ok := merged.Releases["1.0.0"]; !ok {
			t.Fatal("老版本被冲掉了")
		}
	})

	t.Run("同版本内容不同要报错", func(t *testing.T) {
		old := validApp()
		fresh := validApp()
		fresh.Releases["1.0.0"].Packages["x86"] = Package{
			DownloadURL: "https://example.com/other.fpk",
			SHA256:      strings.Repeat("f", 64),
			Size:        1,
		}
		if _, err := MergeApp(old, fresh); err == nil {
			t.Fatal("应该报错")
		}
	})

	t.Run("新条目没摘要就沿用旧的", func(t *testing.T) {
		old := validApp()
		fresh := validApp()
		// 只给下载地址，没给摘要（例如包里还没算出来）。
		fresh.Releases["1.0.0"].Packages["x86"] = Package{
			DownloadURL: "https://example.com/Mihomo-1.0.0-x86.fpk",
		}
		merged, err := MergeApp(old, fresh)
		if err != nil {
			t.Fatalf("合并失败: %v", err)
		}
		got := merged.Releases["1.0.0"].Packages["x86"]
		if got.SHA256 != old.Releases["1.0.0"].Packages["x86"].SHA256 {
			t.Fatalf("摘要没沿用: %q", got.SHA256)
		}
		if got.Size != old.Releases["1.0.0"].Packages["x86"].Size {
			t.Fatalf("大小没沿用: %d", got.Size)
		}
	})

	t.Run("包没变时发布时间不变", func(t *testing.T) {
		old := validApp()
		oldRel := old.Releases["1.0.0"]
		oldRel.UpdatedAt = "2026-11-26T13:00:00+08:00"
		old.Releases["1.0.0"] = oldRel

		fresh := validApp()
		freshRel := fresh.Releases["1.0.0"]
		freshRel.UpdatedAt = "2026-11-26T15:00:00+08:00"
		fresh.Releases["1.0.0"] = freshRel

		merged, err := MergeApp(old, fresh)
		if err != nil {
			t.Fatalf("合并失败: %v", err)
		}
		if merged.Releases["1.0.0"].UpdatedAt != old.Releases["1.0.0"].UpdatedAt {
			t.Fatalf("包没变却改了发布时间: %q", merged.Releases["1.0.0"].UpdatedAt)
		}
	})
}
