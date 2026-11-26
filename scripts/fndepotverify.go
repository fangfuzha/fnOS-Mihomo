package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"fnos-mihomo/internal/fndepot"
)

// 这里的代码管两件事：
//
// 1. 核对 —— 索引、包、Release 资产三者必须对得上；
// 2. 写入 —— 一次发布一个提交，内容没变的文件不碰。

// ghTreeEntry 是 Git tree 里的一项。
type ghTreeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

// ghDo 调一次 GitHub API。
func ghDo(method, url, token string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return err
	}
	setGitHubHeaders(req, token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s 失败: HTTP %d %s", method, url, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return err
		}
	}
	return nil
}

// gitBlobSHA 算 git blob 的 sha，用来判断文件内容有没有变。
func gitBlobSHA(data []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(data))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// ghGetRef 读分支的头提交与它指向的树。
func ghGetRef(token, owner, repo, branch string) (string, string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/git/ref/heads/%s", githubAPI, owner, repo, urlQueryEscape(branch))
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := ghDo(http.MethodGet, url, token, nil, &ref); err != nil {
		return "", "", err
	}
	if ref.Object.SHA == "" {
		return "", "", fmt.Errorf("%s/%s 的分支 %s 不存在", repo, owner, branch)
	}
	url = fmt.Sprintf("%s/repos/%s/%s/git/commits/%s", githubAPI, owner, repo, ref.Object.SHA)
	var commit struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := ghDo(http.MethodGet, url, token, nil, &commit); err != nil {
		return "", "", err
	}
	return ref.Object.SHA, commit.Tree.SHA, nil
}

// ghGetPathSHA 取分支上某个路径当前内容的 blob sha。
//
// 文件不存在时返回空串（不算错误）。
func ghGetPathSHA(token, owner, repo, branch, path string) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/contents/%s?ref=%s", githubAPI, owner, repo, path, urlQueryEscape(branch))
	var out struct {
		SHA string `json:"sha"`
	}
	err := ghDo(http.MethodGet, url, token, nil, &out)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return "", nil
		}
		return "", err
	}
	return out.SHA, nil
}

// ghCreateBlob 上传一份内容，返回 blob 的 sha。
func ghCreateBlob(token, owner, repo string, content []byte) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/git/blobs", githubAPI, owner, repo)
	body := map[string]any{
		"content":  base64Encode(content),
		"encoding": "base64",
	}
	var out struct {
		SHA string `json:"sha"`
	}
	if err := ghDo(http.MethodPost, url, token, body, &out); err != nil {
		return "", err
	}
	return out.SHA, nil
}

// ghCreateTree 在基线树上加若干文件，返回新树的 sha。
func ghCreateTree(token, owner, repo, baseTree string, entries []ghTreeEntry) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/git/trees", githubAPI, owner, repo)
	body := map[string]any{
		"base_tree": baseTree,
		"tree":      entries,
	}
	var out struct {
		SHA string `json:"sha"`
	}
	if err := ghDo(http.MethodPost, url, token, body, &out); err != nil {
		return "", err
	}
	return out.SHA, nil
}

// ghCreateCommit 建一个提交。
func ghCreateCommit(token, owner, repo, message, tree string, parents []string) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/git/commits", githubAPI, owner, repo)
	body := map[string]any{
		"message": message,
		"tree":    tree,
		"parents": parents,
	}
	var out struct {
		SHA string `json:"sha"`
	}
	if err := ghDo(http.MethodPost, url, token, body, &out); err != nil {
		return "", err
	}
	return out.SHA, nil
}

// ghUpdateRef 把分支指到新提交。
func ghUpdateRef(token, owner, repo, branch, sha string) error {
	url := fmt.Sprintf("%s/repos/%s/%s/git/refs/heads/%s", githubAPI, owner, repo, urlQueryEscape(branch))
	body := map[string]any{"sha": sha, "force": false}
	return ghDo(http.MethodPatch, url, token, body, nil)
}

// ghCommitFiles 把若干文件写进分支，一次提交。
//
// 内容没变的文件直接跳过：图标每次发版都一样，
// 每次都提交一遍只会让源仓库的提交历史平白多一倍。
//
// 返回真正改动的文件数；为 0 表示这次不需要提交。
func ghCommitFiles(token, repo, branch, message string, files map[string][]byte) (int, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return 0, err
	}
	head, tree, err := ghGetRef(token, owner, name, branch)
	if err != nil {
		return 0, err
	}
	entries := make([]ghTreeEntry, 0, len(files))
	changed := 0
	for p, content := range files {
		cur, err := ghGetPathSHA(token, owner, name, branch, p)
		if err != nil {
			return 0, err
		}
		if cur != "" && cur == gitBlobSHA(content) {
			continue
		}
		blob, err := ghCreateBlob(token, owner, name, content)
		if err != nil {
			return 0, err
		}
		entries = append(entries, ghTreeEntry{Path: p, Mode: "100644", Type: "blob", SHA: blob})
		changed++
	}
	if changed == 0 {
		return 0, nil
	}
	newTree, err := ghCreateTree(token, owner, name, tree, entries)
	if err != nil {
		return 0, err
	}
	commit, err := ghCreateCommit(token, owner, name, message, newTree, []string{head})
	if err != nil {
		return 0, err
	}
	if err := ghUpdateRef(token, owner, name, branch, commit); err != nil {
		return 0, err
	}
	return changed, nil
}

// releaseAsset 指向一个 GitHub Release 里的资产。
type releaseAsset struct {
	Owner string
	Repo  string
	Tag   string
	Name  string
}

// parseReleaseURL 从下载地址里解析出 Release 资产。
//
// https://github.com/{owner}/{repo}/releases/download/{tag}/{asset}
func parseReleaseURL(raw string) (releaseAsset, bool) {
	const prefix = "https://github.com/"
	if !strings.HasPrefix(raw, prefix) {
		return releaseAsset{}, false
	}
	rest := strings.TrimPrefix(raw, prefix)
	parts := strings.Split(rest, "/")
	if len(parts) < 5 || parts[2] != "releases" || parts[3] != "download" {
		return releaseAsset{}, false
	}
	return releaseAsset{
		Owner: parts[0],
		Repo:  parts[1],
		Tag:   parts[4],
		Name:  strings.Join(parts[5:], "/"),
	}, true
}

// ghReleaseAsset 查一个 Release 资产的大小与摘要。
//
// GitHub 的 API 直接给 digest（sha256）与 size，
// 不用把 50MB 的包下载下来就能核对索引写对了没有。
func ghReleaseAsset(token string, a releaseAsset) (int64, string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases/tags/%s", githubAPI, a.Owner, a.Repo, urlQueryEscape(a.Tag))
	var rel struct {
		Assets []struct {
			Name   string `json:"name"`
			Size   int64  `json:"size"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := ghDo(http.MethodGet, url, token, nil, &rel); err != nil {
		return 0, "", err
	}
	for _, asset := range rel.Assets {
		if asset.Name == a.Name {
			return asset.Size, strings.TrimPrefix(asset.Digest, "sha256:"), nil
		}
	}
	return 0, "", fmt.Errorf("Release %s/%s 的 %s 里没有资产 %s", a.Owner, a.Repo, a.Tag, a.Name)
}

// fndepotVerify 核对索引、包与 Release 资产三者是否对得上。
//
// 推之前核对一次，比推完再发现问题便宜得多：
// 客户端一旦同步到一个对不上的索引，用户看到的要么是装不上，
// 要么是装上了却不是那个版本。
func fndepotVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	file := fs.String("file", filepath.Join("build", "fnpack.json"), "要核对的 fnpack.json")
	icon := fs.String("icon", "", "图标文件")
	version := fs.String("version", "", "要核对的版本（默认取索引里最高的版本）")
	checkRelease := fs.Bool("check-release", false, "用 GitHub API 核对 Release 资产的摘要与大小")
	token := fs.String("token", "", "GitHub token（默认取环境变量 FNDEPOT_TOKEN / GH_TOKEN / GITHUB_TOKEN）")
	var pkgs archFiles = archFiles{}
	fs.Var(&pkgs, "fpk", "本地安装包，格式 架构=路径，可重复")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *token == "" {
		*token = tokenFromEnv()
	}

	b, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	src, err := fndepot.Parse(b)
	if err != nil {
		return err
	}
	var problems []string
	if errs := fndepot.ValidateSource(src); len(errs) > 0 {
		problems = append(problems, joinErrors(errs))
	}

	key := ""
	for k := range src.Apps {
		key = k
		break
	}
	app, _, err := src.App(key)
	if err != nil {
		return err
	}
	ver := *version
	if ver == "" {
		ver = fndepot.LatestVersion(app.Releases)
	}
	if ver == "" {
		return fmt.Errorf("索引里没有版本")
	}
	rel, ok := app.Releases[ver]
	if !ok {
		return fmt.Errorf("索引里没有版本 %s", ver)
	}

	// 本地包：包内 manifest 与索引要对得上，摘要与大小也要对得上。
	for arch, path := range pkgs {
		mf, err := fndepot.ReadFpkManifest(path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		if err := fndepot.CheckPackage(key, ver, arch, mf); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", path, err))
		}
		sha, size, err := fndepot.FileDigest(path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		p, ok := rel.Packages[arch]
		if !ok {
			problems = append(problems, fmt.Sprintf("索引里没有 %s 的 %s 包", ver, arch))
			continue
		}
		if p.SHA256 != "" && p.SHA256 != sha {
			problems = append(problems, fmt.Sprintf("%s 的摘要与索引不符", path))
		}
		if p.Size != 0 && p.Size != size {
			problems = append(problems, fmt.Sprintf("%s 的大小与索引不符", path))
		}
	}

	// 图标：必须是真 PNG，而且别太大（客户端建议 500KB 以内）。
	if *icon != "" {
		if err := checkPNG(*icon); err != nil {
			problems = append(problems, err.Error())
		}
	}

	// Release 资产：不用下载包就能核对 —— API 直接给摘要与大小。
	if *checkRelease {
		for arch, p := range rel.Packages {
			asset, ok := parseReleaseURL(p.DownloadURL)
			if !ok {
				continue
			}
			size, sha, err := ghReleaseAsset(*token, asset)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", p.DownloadURL, err))
				continue
			}
			if p.Size != 0 && p.Size != size {
				problems = append(problems, fmt.Sprintf("%s 的 %s 资产大小不符：索引 %d，实际 %d", ver, arch, p.Size, size))
			}
			if p.SHA256 != "" && sha != "" && p.SHA256 != sha {
				problems = append(problems, fmt.Sprintf("%s 的 %s 资产摘要不符", ver, arch))
			}
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("核对没通过：\n  %s", strings.Join(problems, "\n  "))
	}
	fmt.Printf("==> %s 核对通过（应用 %s，版本 %s）\n", *file, key, ver)
	return nil
}

// checkPNG 校验文件是不是真 PNG，以及大小是否合适。
func checkPNG(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() > 500*1024 {
		return fmt.Errorf("%s 有 %.1f KB，图标建议小于 500KB", path, float64(info.Size())/1024)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var head [8]byte
	if _, err := io.ReadFull(f, head[:]); err != nil {
		return err
	}
	if !bytes.HasPrefix(head[:], []byte("\x89PNG\r\n\x1a\n")) {
		return fmt.Errorf("%s 不是 PNG 文件", path)
	}
	return nil
}

// tokenFromEnv 按优先级取 GitHub token。
func tokenFromEnv() string {
	for _, k := range []string{"FNDEPOT_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// base64Encode 给 GitHub API 传二进制内容。
func base64Encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}
