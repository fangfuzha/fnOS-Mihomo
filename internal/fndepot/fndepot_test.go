package fndepot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 合法应用的骨架，改一处就能测出校验逻辑。
func validApp() App {
	return App{
		DisplayName: "Mihomo 代理",
		Desc:        "在飞牛 NAS 上运行 Mihomo 内核的本地代理应用。",
		Platform:    []string{"x86", "arm"},
		Categories:  []string{"系统工具"},
		IconURL:     "./assets/Mihomo.png",
		RunAs:       "root",
		InstallType: "root",
		IsDocker:    false,
		Releases: map[string]Release{
			"1.0.0": {
				Changelog: "首个版本",
				UpdatedAt: "2026-11-26T12:00:00+08:00",
				Packages: map[string]Package{
					"x86": {DownloadURL: "https://example.com/Mihomo-1.0.0-x86.fpk", SHA256: strings.Repeat("a", 64), Size: 100},
					"arm": {DownloadURL: "https://example.com/Mihomo-1.0.0-arm64.fpk", SHA256: strings.Repeat("b", 64), Size: 200},
				},
			},
		},
	}
}

func TestParse(t *testing.T) {
	t.Run("合法 V2 源", func(t *testing.T) {
		raw := `{
  "schema_version": "2",
  "source_info": {"name": "测试源", "author": "me"},
  "apps": {"Mihomo": {"display_name": "Mihomo"}}
}`
		src, err := Parse([]byte(raw))
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		if src.SourceInfo.Name != "测试源" {
			t.Fatalf("源名称不对: %q", src.SourceInfo.Name)
		}
	})

	t.Run("schema_version 必须是字符串 2", func(t *testing.T) {
		for _, raw := range []string{
			`{"schema_version": 2, "source_info": {"name": "a", "author": "b"}, "apps": {"x": {}}}`,
			`{"schema_version": "2.0", "source_info": {"name": "a", "author": "b"}, "apps": {"x": {}}}`,
			`{"source_info": {"name": "a", "author": "b"}, "apps": {"x": {}}}`,
		} {
			if _, err := Parse([]byte(raw)); err == nil {
				t.Fatalf("应该被拒绝: %s", raw)
			}
		}
	})

	t.Run("源信息与 apps 不能缺", func(t *testing.T) {
		if _, err := Parse([]byte(`{"schema_version":"2","source_info":{"author":"b"},"apps":{"x":{}}}`)); err == nil {
			t.Fatal("缺 source_info.name 应该报错")
		}
		if _, err := Parse([]byte(`{"schema_version":"2","source_info":{"name":"a"},"apps":{"x":{}}}`)); err == nil {
			t.Fatal("缺 source_info.author 应该报错")
		}
		if _, err := Parse([]byte(`{"schema_version":"2","source_info":{"name":"a","author":"b"},"apps":{}}`)); err == nil {
			t.Fatal("apps 为空应该报错")
		}
	})

	t.Run("非法 JSON", func(t *testing.T) {
		if _, err := Parse([]byte("{ 不是 JSON")); err == nil {
			t.Fatal("应该报错")
		}
	})
}

// 合并时别的应用条目必须原样保留 —— 源仓库里通常有不止一个应用。
func TestSetAppKeepsOtherApps(t *testing.T) {
	raw := `{
  "schema_version": "2",
  "source_info": {"name": "测试源", "author": "me"},
  "apps": {
    "Other": {"display_name": "别的应用", "未来才有的字段": {"a": 1}}
  }
}`
	src, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if err := src.SetApp("Mihomo", validApp()); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	out, err := src.Marshal()
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var back Source
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	other, ok := back.Apps["Other"]
	if !ok {
		t.Fatal("别的应用被弄丢了")
	}
	if !strings.Contains(string(other), "未来才有的字段") {
		t.Fatalf("别的应用的未知字段丢了: %s", other)
	}
	if _, ok := back.Apps["Mihomo"]; !ok {
		t.Fatal("新应用没写进去")
	}
}

// 同一个版本号不允许换内容：客户端有缓存，悄悄替换会有人永远装到旧包。
func TestMergeRelease(t *testing.T) {
	app := validApp()
	rel := app.Releases["1.0.0"]

	t.Run("同版本同内容视为重复发布", func(t *testing.T) {
		if _, err := MergeRelease(app, "1.0.0", rel); err != nil {
			t.Fatalf("不应该报错: %v", err)
		}
	})

	t.Run("同版本不同内容必须报错", func(t *testing.T) {
		changed := rel
		changed.Packages = map[string]Package{
			"x86": {DownloadURL: "https://example.com/other.fpk", SHA256: strings.Repeat("c", 64), Size: 1},
		}
		if _, err := MergeRelease(app, "1.0.0", changed); err == nil {
			t.Fatal("应该报错")
		}
	})

	t.Run("新版本并进去，老版本留着", func(t *testing.T) {
		next := Release{Packages: map[string]Package{
			"x86": {DownloadURL: "https://example.com/Mihomo-1.1.0-x86.fpk", SHA256: strings.Repeat("d", 64), Size: 300},
		}}
		merged, err := MergeRelease(app, "1.1.0", next)
		if err != nil {
			t.Fatalf("合并失败: %v", err)
		}
		if len(merged.Releases) != 2 {
			t.Fatalf("版本数不对: %d", len(merged.Releases))
		}
		if _, ok := merged.Releases["1.0.0"]; !ok {
			t.Fatal("老版本被冲掉了")
		}
	})
}

func TestValidate(t *testing.T) {
	t.Run("合法应用没有错误", func(t *testing.T) {
		if errs := Validate("Mihomo", validApp()); len(errs) > 0 {
			t.Fatalf("不该有错误: %v", errs)
		}
	})

	cases := []struct {
		name string
		mut  func(*App)
	}{
		{"分类非法", func(a *App) { a.Categories = []string{"随便起"} }},
		{"分类超过两个", func(a *App) { a.Categories = []string{"系统工具", "AI赋能", "游戏地带"} }},
		{"架构非法", func(a *App) { a.Platform = []string{"x86_64"} }},
		{"run_as 非法", func(a *App) { a.RunAs = "nobody" }},
		{"install_type 非法", func(a *App) { a.InstallType = "存储空间" }},
		{"图标缺失", func(a *App) { a.IconURL = "" }},
		{"图标协议非法", func(a *App) { a.IconURL = "ftp://example.com/a.png" }},
		{"简介为空", func(a *App) { a.Desc = "" }},
		{"没有版本", func(a *App) { a.Releases = map[string]Release{} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			app := validApp()
			c.mut(&app)
			if errs := Validate("Mihomo", app); len(errs) == 0 {
				t.Fatalf("应该报错")
			}
		})
	}

	t.Run("包级错误", func(t *testing.T) {
		app := validApp()
		rel := app.Releases["1.0.0"]
		rel.Packages["x86"] = Package{DownloadURL: "", SHA256: "短", Size: -1}
		app.Releases["1.0.0"] = rel
		errs := Validate("Mihomo", app)
		if len(errs) < 3 {
			t.Fatalf("应该报出多个问题，实际 %d 个: %v", len(errs), errs)
		}
	})
}

func TestFileDigest(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.fpk")
	if err := os.WriteFile(p, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha, size, err := FileDigest(p)
	if err != nil {
		t.Fatalf("算摘要失败: %v", err)
	}
	// echo -n hello | sha256sum
	const want = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if sha != want {
		t.Fatalf("sha256 不对: %s", sha)
	}
	if size != 5 {
		t.Fatalf("大小不对: %d", size)
	}
}

// 源级校验：单个应用不合法只影响那个应用。
func TestValidateSource(t *testing.T) {
	src := &Source{SchemaVersion: SchemaVersion, SourceInfo: SourceInfo{Name: "源", Author: "我"}, Apps: map[string]json.RawMessage{}}
	if err := src.SetApp("Good", validApp()); err != nil {
		t.Fatal(err)
	}
	bad := validApp()
	bad.Categories = []string{"乱写"}
	if err := src.SetApp("Bad", bad); err != nil {
		t.Fatal(err)
	}
	errs := ValidateSource(src)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "Bad") {
		t.Fatalf("应该只报 Bad: %v", errs)
	}
}
