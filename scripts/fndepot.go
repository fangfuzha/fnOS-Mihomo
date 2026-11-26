package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fnos-mihomo/internal/fndepot"
	"fnos-mihomo/internal/safeio"
)

// FnDepot 外部源相关。
//
// 客户端只认源仓库里的 fnpack.json：它按架构和系统版本过滤后，
// 选 releases 里版本号最高的那一版，再和 NAS 上已装的版本比较来提示升级。
// 所以发版流程里必须有一步「把新版本写进 fnpack.json」，
// 只推 GitHub Release 客户端是看不到新版的。
//
// 这里提供三个子命令：
//
//	fndepot build    生成/更新 fnpack.json（本地文件）
//	fndepot publish  把 fnpack.json 与图标推到源仓库（GitHub API）
//	fndepot check    按 V2 规范校验 fnpack.json

// defaultSourceRepo 是默认的应用源仓库。
//
// 仓库名必须含 fndepot：官方每天跑一遍的筛选脚本
// 按仓库名和 fnpack.json 找候选源，名字对不上就不会被发现。
const defaultSourceRepo = "fangfuzha/FnDepot"

// githubAPI 是 GitHub REST API 的根地址。
const githubAPI = "https://api.github.com"

// fndepotOpts 是 fndepot 子命令的公共参数。
type fndepotOpts struct {
	out       string
	merge     string
	config    string
	dryRun    bool
	fresh     bool
	repo      string
	branch    string
	token     string
	fpk       archFiles
	previews  stringList
	icon      string
	release   string
	manifest  string
	privilege string

	sourceName        string
	sourceAuthor      string
	sourceHomepage    string
	sourceDescription string

	appKey     string
	version    string
	display    string
	desc       string
	categories stringList
	platform   stringList
	readmeURL  string
	bugURL     string
}

// archFiles 收集 --fpk 架构=路径 的重复参数。
type archFiles map[string]string

func (a archFiles) String() string { return "" }

func (a archFiles) Set(v string) error {
	i := strings.Index(v, "=")
	if i <= 0 {
		return fmt.Errorf("参数格式应为 架构=路径，实际是 %q", v)
	}
	a[strings.TrimSpace(v[:i])] = strings.TrimSpace(v[i+1:])
	return nil
}

// stringList 收集可重复的字符串参数。
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func cmdFnDepot(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("用法: fndepot build|publish|check [参数...]")
	}
	switch args[0] {
	case "build":
		return fndepotBuild(args[1:])
	case "publish":
		return fndepotPublish(args[1:])
	case "check":
		return fndepotCheck(args[1:])
	case "verify":
		return fndepotVerify(args[1:])
	default:
		return fmt.Errorf("未知子命令 %q，可用：build / publish / check", args[0])
	}
}

// parseFnDepotFlags 解析公共参数。
func parseFnDepotFlags(name string, args []string) (*fndepotOpts, error) {
	// fpk 是 map：得先建好，否则 flag 往 nil map 里写会 panic。
	o := &fndepotOpts{fpk: archFiles{}}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.StringVar(&o.out, "out", "", "fnpack.json 的输出路径（build 用）")
	fs.StringVar(&o.merge, "merge", "", "作为底稿的 fnpack.json（build 用，默认取 --out 已有文件）")
	fs.StringVar(&o.repo, "repo", "", "应用源仓库，格式 owner/name（默认取配置文件）")
	fs.StringVar(&o.branch, "branch", "", "应用源仓库的分支（默认取配置文件）")
	fs.StringVar(&o.config, "config", "", "源配置文件（默认 fndepot/source.json）")
	fs.BoolVar(&o.fresh, "fresh", false, "build 用：不拿已有的 fnpack.json 当底稿，重新生成")
	fs.StringVar(&o.token, "token", "", "GitHub token（默认取环境变量 FNDEPOT_TOKEN / GH_TOKEN / GITHUB_TOKEN）")
	fs.BoolVar(&o.dryRun, "dry-run", false, "只打印将要推送的内容，不真的写")
	fs.Var(&o.fpk, "fpk", "安装包，格式 架构=路径（x86 / arm / all），可重复")
	fs.Var(&o.previews, "preview", "预览图路径，可重复")
	fs.StringVar(&o.icon, "icon", "", "图标文件（默认 fnos/ICON.PNG）")
	fs.StringVar(&o.release, "release-base", "", "安装包下载地址前缀（默认按 GitHub Release 规则推导）")
	fs.StringVar(&o.manifest, "manifest", "", "fnOS 包的 manifest（默认 fnos/manifest）")
	fs.StringVar(&o.privilege, "privilege", "", "fnOS 包的 privilege 配置（默认 fnos/config/privilege）")
	fs.StringVar(&o.sourceName, "source-name", "", "源名称")
	fs.StringVar(&o.sourceAuthor, "source-author", "", "源作者")
	fs.StringVar(&o.sourceHomepage, "source-homepage", "", "源主页")
	fs.StringVar(&o.sourceDescription, "source-description", "", "源简介")
	fs.StringVar(&o.appKey, "app-key", "", "应用在源里的键（默认取 manifest 的 appname）")
	fs.StringVar(&o.version, "version", "", "版本号（默认取 manifest 的 version）")
	fs.StringVar(&o.display, "display-name", "", "显示名称（默认取 manifest 的 display_name）")
	fs.StringVar(&o.desc, "desc", "", "应用简介（默认取 manifest 的 desc）")
	fs.Var(&o.categories, "categories", "分类，逗号分隔（默认 系统工具）")
	fs.Var(&o.platform, "platform", "架构列表，逗号分隔（默认按 --fpk 推导）")
	fs.StringVar(&o.readmeURL, "readme-url", "", "应用详情页地址")
	fs.StringVar(&o.bugURL, "bug-report-url", "", "问题反馈地址")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	// 配置文件是源元数据的唯一出处：CI 与本地读同一份，
	// 免得两边各写一遍、写着写着就不一致了。
	if err := o.loadConfig(); err != nil {
		return nil, err
	}
	if o.repo == "" {
		o.repo = defaultSourceRepo
	}
	if o.branch == "" {
		o.branch = "main"
	}
	if o.token == "" {
		for _, k := range []string{"FNDEPOT_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
			if v := os.Getenv(k); v != "" {
				o.token = v
				break
			}
		}
	}
	if o.categories == nil {
		o.categories = stringList{"系统工具"}
	}
	return o, nil
}

// fndepotConfig 是应用源的默认配置（fndepot/source.json）。
//
// 源名称、作者、分类这些跟代码无关的东西放在配置里，
// CI 与本地读同一份，不会写着写着两边不一样。
type fndepotConfig struct {
	Repo         string             `json:"repo"`
	Branch       string             `json:"branch"`
	SourceInfo   fndepot.SourceInfo `json:"source_info"`
	Categories   []string           `json:"categories"`
	ReadmeURL    string             `json:"readme_url"`
	BugReportURL string             `json:"bug_report_url"`
}

// loadConfig 读配置文件，补齐命令行没给的字段。
func (o *fndepotOpts) loadConfig() error {
	path := o.config
	if path == "" {
		path = filepath.Join(repoRoot(), "fndepot", "source.json")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var c fndepotConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return fmt.Errorf("读 %s 失败: %w", path, err)
	}
	if o.repo == "" {
		o.repo = c.Repo
	}
	if o.branch == "" {
		o.branch = c.Branch
	}
	if o.sourceName == "" {
		o.sourceName = c.SourceInfo.Name
	}
	if o.sourceAuthor == "" {
		o.sourceAuthor = c.SourceInfo.Author
	}
	if o.sourceHomepage == "" {
		o.sourceHomepage = c.SourceInfo.Homepage
	}
	if o.sourceDescription == "" {
		o.sourceDescription = c.SourceInfo.Description
	}
	if len(o.categories) == 0 && len(c.Categories) > 0 {
		o.categories = stringList(c.Categories)
	}
	if o.readmeURL == "" {
		o.readmeURL = c.ReadmeURL
	}
	if o.bugURL == "" {
		o.bugURL = c.BugReportURL
	}
	return nil
}

// fndepotBuild 生成（或更新）本地 fnpack.json。
func fndepotBuild(args []string) error {
	o, err := parseFnDepotFlags("build", args)
	if err != nil {
		return err
	}
	if o.out == "" {
		o.out = filepath.Join("build", "fnpack.json")
	}
	built, err := buildSource(o)
	if err != nil {
		return err
	}
	// 底稿：--merge 指定的文件，没有就用 --out 已有的那份。
	// 重新生成一次索引不能把老版本弄丢 —— 源仓库里通常还有别的应用。
	// --fresh 则不要底稿：本地残留的旧文件会把重新生成堵住。
	var src *fndepot.Source
	if !o.fresh {
		base := o.merge
		if base == "" {
			base = o.out
		}
		src, err = loadSource(base)
		if err != nil {
			return err
		}
	}
	if src == nil {
		src = built
	} else {
		if src.SourceInfo.Name == "" {
			src.SourceInfo = built.SourceInfo
		}
		if err := mergeApp(src, o, built); err != nil {
			return err
		}
	}
	data, err := src.Marshal()
	if err != nil {
		return err
	}
	if err := safeio.WriteFile(o.out, data, 0o644); err != nil {
		return fmt.Errorf("写 %s 失败: %w", o.out, err)
	}
	fmt.Printf("==> 已生成 %s（应用 %s，共 %d 个应用）\n", o.out, o.appKey, len(src.Apps))
	return nil
}

// fndepotPublish 把 fnpack.json 与图标推到应用源仓库。
func fndepotPublish(args []string) error {
	o, err := parseFnDepotFlags("publish", args)
	if err != nil {
		return err
	}
	if o.token == "" {
		return fmt.Errorf("没有 GitHub token：请设置环境变量 FNDEPOT_TOKEN，或加 --token")
	}
	// 远端已有的 fnpack.json 是底稿：别的应用的条目要原样保留，
	// 自己只改自己那一条。
	base, err := ghGetContent(o.token, o.repo, o.branch, "fnpack.json")
	if err != nil {
		return err
	}
	var src *fndepot.Source
	if base != nil {
		src, err = fndepot.Parse([]byte(base.Content))
		if err != nil {
			return fmt.Errorf("远端 fnpack.json 解析失败: %w", err)
		}
	} else {
		src = &fndepot.Source{
			SchemaVersion: fndepot.SchemaVersion,
			SourceInfo:    fndepot.SourceInfo{Name: o.sourceName, Author: o.sourceAuthor},
			Apps:          map[string]json.RawMessage{},
		}
	}
	if src.SourceInfo.Name == "" {
		src.SourceInfo.Name = o.sourceName
	}
	if src.SourceInfo.Author == "" {
		src.SourceInfo.Author = o.sourceAuthor
	}
	if o.sourceHomepage != "" {
		src.SourceInfo.Homepage = o.sourceHomepage
	}
	if o.sourceDescription != "" {
		src.SourceInfo.Description = o.sourceDescription
	}

	built, err := buildSource(o)
	if err != nil {
		return err
	}
	if err := mergeApp(src, o, built); err != nil {
		return err
	}
	if errs := fndepot.ValidateSource(src); len(errs) > 0 {
		return fmt.Errorf("生成的 fnpack.json 不合规:\n  %s", joinErrors(errs))
	}
	data, err := src.Marshal()
	if err != nil {
		return err
	}
	if o.dryRun {
		fmt.Printf("==> dry-run：不会写任何文件。将要推送 %s/%s 的 fnpack.json：\n%s\n", o.repo, o.branch, data)
		for _, a := range buildAssets(o) {
			fmt.Printf("==> dry-run：将上传 %s（来自 %s）\n", a.remote, a.local)
		}
		return nil
	}
	// 一次发布一个提交：索引与图标进同一个 commit。
	// 分两次写就是两个提交，源仓库的提交历史会平白多一倍。
	// 内容没变的文件（图标每次都一样）直接跳过，连提交都不产生。
	files := map[string][]byte{"fnpack.json": data}
	for _, a := range buildAssets(o) {
		b, err := os.ReadFile(a.local)
		if err != nil {
			return err
		}
		files[a.remote] = b
	}
	changed, err := ghCommitFiles(o.token, o.repo, o.branch, "更新 FnDepot 应用源索引", files)
	if err != nil {
		return err
	}
	if changed == 0 {
		fmt.Printf("==> %s/%s 的内容没有变化，不产生新提交\n", o.repo, o.branch)
		return nil
	}
	fmt.Printf("==> 已提交 %s/%s（%d 个文件）\n", o.repo, o.branch, changed)

	// 写完回读：推成功不等于写对了 —— 远端拿到的内容要真的对上。
	if err := verifyRemote(o.token, o.repo, o.branch, o.appKey, src); err != nil {
		return err
	}
	fmt.Printf("==> 已回读校验 %s/%s 的 fnpack.json，内容一致\n", o.repo, o.branch)
	return nil
}

// verifyRemote 回读远端的索引，确认与要推的内容一致。
//
// 推成功不等于写对了：分支保护、并发推送、内容编码都可能让远端
// 拿到别的东西。回读一次才算数。
func verifyRemote(token, repo, branch, appKey string, want *fndepot.Source) error {
	cur, err := ghGetContent(token, repo, branch, "fnpack.json")
	if err != nil {
		return err
	}
	if cur == nil {
		return fmt.Errorf("远端没有 fnpack.json")
	}
	got, err := fndepot.Parse([]byte(cur.Content))
	if err != nil {
		return fmt.Errorf("远端 fnpack.json 解析失败: %w", err)
	}
	wantApp, _, err := want.App(appKey)
	if err != nil {
		return err
	}
	gotApp, _, err := got.App(appKey)
	if err != nil {
		return err
	}
	if gotApp.ReadmeURL != wantApp.ReadmeURL {
		return fmt.Errorf("远端 readme_url 不一致：本地 %q，远端 %q", wantApp.ReadmeURL, gotApp.ReadmeURL)
	}
	for v, w := range wantApp.Releases {
		g, ok := gotApp.Releases[v]
		if !ok {
			return fmt.Errorf("远端少了版本 %s", v)
		}
		for arch, wp := range w.Packages {
			gp, ok := g.Packages[arch]
			if !ok {
				return fmt.Errorf("远端版本 %s 少了 %s 包", v, arch)
			}
			if gp.DownloadURL != wp.DownloadURL || gp.SHA256 != wp.SHA256 || gp.Size != wp.Size {
				return fmt.Errorf("远端版本 %s 的 %s 包与本地不一致", v, arch)
			}
		}
	}
	return nil
}

// loadSource 读一份已有的 fnpack.json 当底稿。
//
// 文件不存在时返回 (nil, nil)：第一次生成时没有底稿，属正常情况。
func loadSource(path string) (*fndepot.Source, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	src, err := fndepot.Parse(b)
	if err != nil {
		return nil, fmt.Errorf("读 %s 失败: %w", path, err)
	}
	return src, nil
}

// mergeApp 把当前应用并进一个已有的源。
//
// 别的应用条目与老版本都留着：源仓库通常有不止一个应用，
// 也不能因为重新生成一次索引就把历史版本弄丢。
func mergeApp(src *fndepot.Source, o *fndepotOpts, built *fndepot.Source) error {
	fresh, _, err := built.App(o.appKey)
	if err != nil {
		return err
	}
	old, has, err := src.App(o.appKey)
	if err != nil {
		return err
	}
	app := fresh
	if has {
		// 应用级字段取新的，老版本留作历史。
		if app, err = fndepot.MergeApp(old, fresh); err != nil {
			return err
		}
	}
	return src.SetApp(o.appKey, app)
}

// fndepotCheck 校验一个 fnpack.json 是否符合 V2 规范。
func fndepotCheck(args []string) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	file := fs.String("file", filepath.Join("build", "fnpack.json"), "要校验的 fnpack.json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	b, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	src, err := fndepot.Parse(b)
	if err != nil {
		return err
	}
	errs := fndepot.ValidateSource(src)
	if len(errs) > 0 {
		return fmt.Errorf("%s 不合规:\n  %s", *file, joinErrors(errs))
	}
	fmt.Printf("==> %s 合规（%d 个应用）\n", *file, len(src.Apps))
	return nil
}

// buildSource 按参数组装一个源（只含当前应用）。
func buildSource(o *fndepotOpts) (*fndepot.Source, error) {
	root := repoRoot()
	if o.manifest == "" {
		o.manifest = filepath.Join(root, "fnos", "manifest")
	}
	if o.privilege == "" {
		o.privilege = filepath.Join(root, "fnos", "config", "privilege")
	}
	mf, err := parseManifest(o.manifest)
	if err != nil {
		return nil, err
	}
	if o.appKey == "" {
		o.appKey = mf["appname"]
	}
	if o.appKey == "" {
		return nil, fmt.Errorf("应用键为空：manifest 里没有 appname")
	}
	if o.version == "" {
		o.version = mf["version"]
	}
	if o.version == "" {
		return nil, fmt.Errorf("版本号为空：manifest 里没有 version")
	}
	if o.display == "" {
		o.display = mf["display_name"]
	}
	if o.desc == "" {
		o.desc = mf["desc"]
	}

	// README 与问题反馈地址：能从仓库地址推出来就推，
	// 客户端详情页上这两个入口挺常用。
	repoURL := mf["distributor_url"]
	if repoURL == "" {
		repoURL = mf["maintainer_url"]
	}
	if strings.Contains(repoURL, "github.com") {
		if o.readmeURL == "" {
			o.readmeURL = deriveReadmeURL(repoURL)
		}
		if o.bugURL == "" {
			o.bugURL = deriveBugURL(repoURL)
		}
	}

	runAs := "root"
	if v, err := readRunAs(o.privilege); err == nil && v != "" {
		runAs = v
	}
	installType := ""
	if runAs == "root" {
		// V2 里 install_type 只有两种：空串是存储空间，root 是系统空间。
		// 以 root 身份跑的应用装在系统空间。
		installType = "root"
	}

	release, err := o.buildRelease()
	if err != nil {
		return nil, err
	}

	app := fndepot.App{
		DisplayName:    o.display,
		Desc:           o.desc,
		Platform:       o.appPlatform(),
		Categories:     []string(o.categories),
		IconURL:        "./assets/" + o.appKey + ".png",
		ReadmeURL:      o.readmeURL,
		BugReportURL:   o.bugURL,
		Maintainer:     mf["maintainer"],
		MaintainerURL:  mf["maintainer_url"],
		Distributor:    mf["distributor"],
		DistributorURL: mf["distributor_url"],
		RunAs:          runAs,
		InstallType:    installType,
		IsDocker:       false,
		ServicePort:    mf["service_port"],
		Releases:       map[string]fndepot.Release{o.version: release},
	}
	if o.previews != nil {
		for i := range o.previews {
			app.PreviewURLs = append(app.PreviewURLs, fmt.Sprintf("./assets/%s-preview-%d.png", o.appKey, i+1))
		}
	}

	src := &fndepot.Source{
		SchemaVersion: fndepot.SchemaVersion,
		SourceInfo: fndepot.SourceInfo{
			Name:        o.sourceName,
			Author:      o.sourceAuthor,
			Homepage:    o.sourceHomepage,
			Description: o.sourceDescription,
		},
		Apps: map[string]json.RawMessage{},
	}
	if err := src.SetApp(o.appKey, app); err != nil {
		return nil, err
	}
	return src, nil
}

// buildRelease 组装版本条目：算出每个包的 sha256 与字节数。
func (o *fndepotOpts) buildRelease() (fndepot.Release, error) {
	if len(o.fpk) == 0 {
		return fndepot.Release{}, fmt.Errorf("至少要给一个 --fpk 架构=路径")
	}
	rel := fndepot.Release{
		Changelog:    o.changelog(),
		UpdatedAt:    time.Now().Format("2006-01-02T15:04:05-07:00"),
		OSMinVersion: o.osMinVersion(),
		Packages:     map[string]fndepot.Package{},
	}
	base := ""
	for arch, p := range o.fpk {
		// 值是 http/https 时当成完整下载地址：包还没构建出来时先挂上位置，
		// 等第一次发版，CI 会带着 sha256 与 size 重新发布这条。
		if isRemoteURL(p) {
			rel.Packages[arch] = fndepot.Package{DownloadURL: p}
			continue
		}
		if base == "" {
			var err error
			if base, err = o.releaseBase(); err != nil {
				return fndepot.Release{}, err
			}
		}
		// 包里的 manifest 与索引必须对得上：
		// 键不符客户端会跳过这个应用，版本不符装上去就是另一个版本。
		mf, err := fndepot.ReadFpkManifest(p)
		if err != nil {
			return fndepot.Release{}, fmt.Errorf("读 %s 的 manifest 失败: %w", p, err)
		}
		if err := fndepot.CheckPackage(o.appKey, o.version, arch, mf); err != nil {
			return fndepot.Release{}, err
		}
		sha, size, err := fndepot.FileDigest(p)
		if err != nil {
			return fndepot.Release{}, fmt.Errorf("算 %s 的摘要失败: %w", p, err)
		}
		rel.Packages[arch] = fndepot.Package{
			DownloadURL: strings.TrimRight(base, "/") + "/" + filepath.Base(p),
			SHA256:      sha,
			Size:        size,
		}
	}
	return rel, nil
}

// deriveReadmeURL 从仓库地址推出 README 的原始文件地址。
//
// 要的是 raw 地址，不是 blob 页面：blob 是 HTML 页，
// 客户端把整页（含导航与页脚）当 Markdown 渲染，详情页会一塌糊涂。
func deriveReadmeURL(repoURL string) string {
	trimmed := strings.TrimRight(repoURL, "/")
	// https://github.com/owner/repo → https://raw.githubusercontent.com/owner/repo
	rest := strings.TrimPrefix(trimmed, "https://github.com/")
	if rest == trimmed {
		return trimmed + "/blob/main/README.md"
	}
	return "https://raw.githubusercontent.com/" + rest + "/main/README.md"
}

// deriveBugURL 从仓库地址推出问题反馈地址。
func deriveBugURL(repoURL string) string {
	return strings.TrimRight(repoURL, "/") + "/issues"
}

// isRemoteURL 判断是不是完整的远程地址。
func isRemoteURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// appPlatform 推出应用级架构列表。
func (o *fndepotOpts) appPlatform() []string {
	if len(o.platform) > 0 {
		return []string(o.platform)
	}
	out := make([]string, 0, len(o.fpk))
	for arch := range o.fpk {
		out = append(out, arch)
	}
	sortStrings(out)
	return out
}

// releaseBase 推导安装包下载地址前缀。
//
// 默认按 GitHub Release 的规则拼：仓库用 GITHUB_REPOSITORY，
// 标签用 GITHUB_REF_NAME（打 tag 触发时就是 v1.2.3）。
func (o *fndepotOpts) releaseBase() (string, error) {
	if o.release != "" {
		return o.release, nil
	}
	repo := os.Getenv("GITHUB_REPOSITORY")
	if repo == "" {
		return "", fmt.Errorf("无法推导下载地址前缀：请加 --release-base，或在 CI 里带上 GITHUB_REPOSITORY")
	}
	tag := os.Getenv("GITHUB_REF_NAME")
	// 手动触发时 GITHUB_REF_NAME 是分支名，不是标签 —— 那种情况按版本号拼。
	if tag == "" || os.Getenv("GITHUB_REF_TYPE") != "tag" {
		tag = "v" + o.version
	}
	return fmt.Sprintf("https://github.com/%s/releases/download/%s", repo, tag), nil
}

// changelog 取 manifest 里的更新说明。
//
// manifest 的写法是「版本号 说明」，前缀去掉，
// 免得客户端上显示成「1.0.0 1.0.0 首个版本」。
func (o *fndepotOpts) changelog() string {
	mf, err := parseManifest(o.manifest)
	if err != nil {
		return ""
	}
	text := mf["changelog"]
	prefix := o.version + " "
	return strings.TrimPrefix(text, prefix)
}

// osMinVersion 取 manifest 里的最低系统版本。
func (o *fndepotOpts) osMinVersion() string {
	mf, err := parseManifest(o.manifest)
	if err != nil {
		return ""
	}
	return mf["os_min_version"]
}

// assetFile 是要传到源仓库的资源。
type assetFile struct {
	remote string
	local  string
}

// buildAssets 列出要上传到源仓库的资源。
func buildAssets(o *fndepotOpts) []assetFile {
	var out []assetFile
	if o.icon != "" {
		out = append(out, assetFile{remote: "assets/" + o.appKey + ".png", local: o.icon})
	}
	for i, p := range o.previews {
		out = append(out, assetFile{remote: fmt.Sprintf("assets/%s-preview-%d.png", o.appKey, i+1), local: p})
	}
	return out
}

// parseManifest 读 fnOS 包的 manifest。
//
// 格式是 ini 风格：key = value，值里有 = 也不怕，
// 只按第一个 = 切。
func parseManifest(p string) (map[string]string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
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
	return out, nil
}

// readRunAs 读 privilege 配置里的运行身份。
func readRunAs(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	var v struct {
		Defaults struct {
			RunAs string `json:"run-as"`
		} `json:"defaults"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return "", err
	}
	return v.Defaults.RunAs, nil
}

// joinErrors 把错误列表拼成一行行。
func joinErrors(errs []error) string {
	lines := make([]string, 0, len(errs))
	for _, e := range errs {
		lines = append(lines, e.Error())
	}
	return strings.Join(lines, "\n  ")
}

// sortStrings 小工具：排序字符串切片。
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// ghContent 是 GitHub contents API 的一个文件。
type ghContent struct {
	SHA     string `json:"sha"`
	Content string `json:"content"`
}

// ghGetContent 取源仓库里的一个文件。
//
// 文件不存在时返回 (nil, nil)：第一次发布时源仓库是空的，属正常情况。
func ghGetContent(token, repo, branch, p string) (*ghContent, error) {
	url := fmt.Sprintf("%s/repos/%s/contents/%s?ref=%s", githubAPI, repo, p, urlQueryEscape(branch))
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	setGitHubHeaders(req, token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("读取 %s/%s 的 %s 失败: HTTP %d %s", repo, branch, p, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out ghContent
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(out.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("解码 %s 的内容失败: %w", p, err)
	}
	out.Content = string(raw)
	return &out, nil
}

// setGitHubHeaders 加上 GitHub API 要求的请求头。
func setGitHubHeaders(req *http.Request, token string) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "fnOS-Mihomo-fndepot")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

// splitRepo 拆 owner/name。
func splitRepo(repo string) (string, string, error) {
	i := strings.Index(repo, "/")
	if i <= 0 || i == len(repo)-1 {
		return "", "", fmt.Errorf("仓库格式应为 owner/name，实际是 %q", repo)
	}
	return repo[:i], repo[i+1:], nil
}

// urlQueryEscape 转义查询参数里的分支名。
func urlQueryEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.', r == '~':
			b.WriteRune(r)
		default:
			b.WriteString(fmt.Sprintf("%%%02X", r))
		}
	}
	return b.String()
}
