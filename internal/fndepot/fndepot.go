// Package fndepot 处理 FnDepot 外部应用源（V2）的索引文件 fnpack.json。
//
// 客户端只认这个文件：它按设备架构和系统版本过滤后，选 releases 里版本号最高的
// 那一版，再和 NAS 上已安装包的 manifest 版本比较来提示升级。
// 所以「发版」的真相是 —— 版本号必须写进 fnpack.json，
// 只推 GitHub Release 客户端是看不到的。
package fndepot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// SchemaVersion 是 V2 源必须写的版本标识。
//
// 必须是字符串 "2"：客户端只接受这个精确值，
// 写成数字 2 或 "2.0" 都会让整个源同步失败。
const SchemaVersion = "2"

// Categories 是客户端认的固定分类，写别的名字会被判非法。
var Categories = []string{
	"影音娱乐", "系统工具", "编程开发", "AI赋能", "生活服务",
	"智能智控", "教育学习", "游戏地带", "硬件驱动",
}

// Platforms 是合法的架构标识。
//
// x86/arm 是 fnOS 包里 platform 字段的取值；
// all 表示通用包，客户端选包时先找当前架构，找不到才回退到 all。
var Platforms = []string{"all", "x86", "arm"}

// RunAsValues 是合法的运行身份。
var RunAsValues = []string{"package", "root"}

// InstallTypes 是合法的安装位置类型：空串表示存储空间，root 表示系统空间。
var InstallTypes = []string{"", "root"}

// SourceInfo 描述这个应用源本身。
type SourceInfo struct {
	Name        string `json:"name"`
	Author      string `json:"author"`
	Homepage    string `json:"homepage,omitempty"`
	Description string `json:"description,omitempty"`
}

// Package 是一个架构对应的安装包。
type Package struct {
	DownloadURL string `json:"download_url"`
	SHA256      string `json:"sha256,omitempty"`
	Size        int64  `json:"size,omitempty"`
	Changelog   string `json:"changelog,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

// Release 是一个版本的元数据。
type Release struct {
	Changelog    string             `json:"changelog,omitempty"`
	UpdatedAt    string             `json:"updated_at,omitempty"`
	OSMinVersion string             `json:"os_min_version,omitempty"`
	OSMaxVersion string             `json:"os_max_version,omitempty"`
	Packages     map[string]Package `json:"packages"`
}

// App 是一个应用的元数据。
type App struct {
	DisplayName    string             `json:"display_name"`
	Desc           string             `json:"desc"`
	Platform       []string           `json:"platform"`
	Categories     []string           `json:"categories"`
	IconURL        string             `json:"icon_url"`
	PreviewURLs    []string           `json:"preview_urls,omitempty"`
	ReadmeURL      string             `json:"readme_url,omitempty"`
	BugReportURL   string             `json:"bug_report_url,omitempty"`
	Maintainer     string             `json:"maintainer,omitempty"`
	MaintainerURL  string             `json:"maintainer_url,omitempty"`
	Distributor    string             `json:"distributor,omitempty"`
	DistributorURL string             `json:"distributor_url,omitempty"`
	RunAs          string             `json:"run_as"`
	InstallType    string             `json:"install_type"`
	IsDocker       bool               `json:"is_docker"`
	ServicePort    string             `json:"service_port,omitempty"`
	Releases       map[string]Release `json:"releases"`
}

// Source 是 fnpack.json 的根结构。
//
// apps 用 map[string]json.RawMessage 存：合并时只能动自己那一个应用，
// 别的应用条目里的字段原样保留，不能因为重新序列化就丢掉未知字段。
type Source struct {
	SchemaVersion string                     `json:"schema_version"`
	SourceInfo    SourceInfo                 `json:"source_info"`
	Apps          map[string]json.RawMessage `json:"apps"`
}

// Parse 解析并做结构级校验。
//
// 源级错误会让客户端整个源同步失败（还会保留旧缓存，应用看起来「消失」），
// 所以这里把能查出来的结构问题提前查出来。
func Parse(data []byte) (*Source, error) {
	var s Source
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("fnpack.json 不是合法 JSON: %w", err)
	}
	if s.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("schema_version 必须是字符串 %q，实际是 %q", SchemaVersion, s.SchemaVersion)
	}
	if strings.TrimSpace(s.SourceInfo.Name) == "" {
		return nil, fmt.Errorf("source_info.name 不能为空")
	}
	if strings.TrimSpace(s.SourceInfo.Author) == "" {
		return nil, fmt.Errorf("source_info.author 不能为空")
	}
	if s.Apps == nil || len(s.Apps) == 0 {
		return nil, fmt.Errorf("apps 不能为空")
	}
	return &s, nil
}

// App 取出一个应用的元数据。
func (s *Source) App(key string) (App, bool, error) {
	raw, ok := s.Apps[key]
	if !ok {
		return App{}, false, nil
	}
	var app App
	if err := json.Unmarshal(raw, &app); err != nil {
		return App{}, true, fmt.Errorf("应用 %q 的元数据解析失败: %w", key, err)
	}
	return app, true, nil
}

// SetApp 写入（或覆盖）一个应用的元数据。
func (s *Source) SetApp(key string, app App) error {
	raw, err := json.Marshal(app)
	if err != nil {
		return fmt.Errorf("应用 %q 序列化失败: %w", key, err)
	}
	if s.Apps == nil {
		s.Apps = map[string]json.RawMessage{}
	}
	s.Apps[key] = raw
	return nil
}

// Marshal 输出 fnpack.json 的内容。
//
// 缩进两空格 + 末尾换行：源文件是要人看也要人改的。
func (s *Source) Marshal() ([]byte, error) {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("fnpack.json 序列化失败: %w", err)
	}
	return append(b, '\n'), nil
}

// MergeRelease 把一个新版本并进已有的应用元数据。
//
// 规范要求「已发布的版本号 + 架构文件视为不可变」：
// 同一个版本号再发一次、但包内容变了，必须报错让人升版本号，
// 不能悄悄替换 —— 客户端有缓存，悄悄替换的结果是有人永远装到旧包。
func MergeRelease(app App, version string, rel Release) (App, error) {
	if app.Releases == nil {
		app.Releases = map[string]Release{}
	}
	old, exists := app.Releases[version]
	if exists {
		for arch, p := range rel.Packages {
			oldPkg, ok := old.Packages[arch]
			if !ok {
				continue
			}
			if oldPkg.SHA256 != "" && p.SHA256 != "" && oldPkg.SHA256 != p.SHA256 {
				return app, fmt.Errorf("版本 %s 的 %s 包已经存在且内容不同，请升版本号再发", version, arch)
			}
		}
	}
	app.Releases[version] = rel
	return app, nil
}

// Validate 按 V2 规范逐项检查一个应用。
//
// 返回所有问题而不是第一个 —— 一次改完比来回试省事。
func Validate(key string, app App) []error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", key, fmt.Sprintf(format, args...)))
	}

	if strings.TrimSpace(app.DisplayName) == "" {
		add("display_name 不能为空")
	}
	if strings.TrimSpace(app.Desc) == "" {
		add("desc 不能为空")
	}
	if len(app.Platform) == 0 {
		add("platform 不能为空")
	}
	for _, p := range app.Platform {
		if !contains(Platforms, p) {
			add("platform 含非法值 %q（只能是 all/x86/arm）", p)
		}
	}
	if len(app.Categories) == 0 {
		add("categories 不能为空")
	}
	if len(app.Categories) > 2 {
		add("categories 最多两个，实际 %d 个", len(app.Categories))
	}
	for _, c := range app.Categories {
		if !contains(Categories, c) {
			add("categories 含非法值 %q", c)
		}
	}
	if strings.TrimSpace(app.IconURL) == "" {
		add("icon_url 不能为空")
	} else if err := checkURL(app.IconURL); err != nil {
		add("icon_url 非法: %v", err)
	}
	if !contains(RunAsValues, app.RunAs) {
		add("run_as 非法: %q（只能是 package/root）", app.RunAs)
	}
	if !contains(InstallTypes, app.InstallType) {
		add("install_type 非法: %q（空串或 root）", app.InstallType)
	}
	if len(app.Releases) == 0 {
		add("releases 不能为空")
	}
	for _, v := range sortedVersions(app.Releases) {
		rel := app.Releases[v]
		if len(rel.Packages) == 0 {
			add("版本 %s 没有 packages", v)
		}
		for arch, p := range rel.Packages {
			if !contains(Platforms, arch) {
				add("版本 %s 的架构键 %q 非法（只能是 all/x86/arm）", v, arch)
			}
			if strings.TrimSpace(p.DownloadURL) == "" {
				add("版本 %s 的 %s 包缺 download_url", v, arch)
			} else if err := checkURL(p.DownloadURL); err != nil {
				add("版本 %s 的 %s 包 download_url 非法: %v", v, arch, err)
			}
			if p.SHA256 != "" {
				if len(p.SHA256) != 64 {
					add("版本 %s 的 %s 包 sha256 长度不是 64", v, arch)
				} else if !isHex(p.SHA256) {
					add("版本 %s 的 %s 包 sha256 不是十六进制", v, arch)
				}
			}
			if p.Size < 0 {
				add("版本 %s 的 %s 包 size 不能为负", v, arch)
			}
		}
	}
	return errs
}

// ValidateSource 检查整个源。
func ValidateSource(s *Source) []error {
	var errs []error
	for key := range s.Apps {
		app, _, err := s.App(key)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		errs = append(errs, Validate(key, app)...)
	}
	return errs
}

// FileDigest 算文件的 sha256 与字节数。
//
// sha256 客户端会强制校验，size 必须写真实字节数 ——
// 写 "20 MB" 这种字符串会让安装包校验失败。
func FileDigest(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// sortedVersions 按可比较的版本号排序输出。
//
// 排序只是为了让校验输出稳定；客户端自己会挑最高版本。
func sortedVersions(releases map[string]Release) []string {
	out := make([]string, 0, len(releases))
	for v := range releases {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func isHex(s string) bool {
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// checkURL 检查下载链接。
//
// 相对路径是允许的（相对 fnpack.json 所在目录解析），
// 但不能带 file/ftp 之类客户端不认的协议。
func checkURL(raw string) error {
	if strings.HasPrefix(raw, "./") || strings.HasPrefix(raw, "../") {
		return nil
	}
	switch {
	case strings.HasPrefix(raw, "http://"), strings.HasPrefix(raw, "https://"):
		return nil
	default:
		return fmt.Errorf("只允许 http/https 或相对路径，实际是 %q", raw)
	}
}
