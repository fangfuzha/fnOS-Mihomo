package dashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"fnos-mihomo/internal/safeio"
)

// manifestName 是本地归档清单的文件名。
//
// 清单和归档放在同一个目录里：换目录就等于换了一套本地副本，
// 版本号跟着归档走，不会出现「归档是新的、清单是旧的」这种错配。
const ManifestName = "dashboards.json"

// commitPattern 从 atom 订阅里抓最新提交的 SHA。
var commitPattern = regexp.MustCompile(`Commit/([0-9a-f]{40})`)

// ArchiveMeta 记录一份本地归档的元信息。
type ArchiveMeta struct {
	// Version 是这份归档对应的上游版本号（release 的 tag 或分支的提交 SHA）。
	Version string `json:"version"`
	// ETag 是下载时远端返回的值，作为二次线索。
	ETag string `json:"etag,omitempty"`
	// Size 与 SHA256 用来确认本地文件没被截断。
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	// FetchedAt 是最近一次真正下载的时间。
	FetchedAt string `json:"fetched_at,omitempty"`
	// CheckedAt 是最近一次探测上游版本的时间。
	//
	// 记在缓存目录的清单里：随包分发的那份是只读的，
	// 但探测节奏是我们自己的运行时状态，不该写进安装目录。
	CheckedAt string `json:"checked_at,omitempty"`
}

// Manifest 是本地归档清单。
type Manifest struct {
	Dashboards map[string]ArchiveMeta `json:"dashboards"`
}

// Cache 管理第三方面板归档的本地副本。
//
// 面板归档有两种来源：
//   - 可写缓存目录：运行时下载的新版本落在这里；
//   - 只读候选目录：随应用包分发的那一份（打包时从 assets/ 拷进去）。
//
// 查找时缓存优先于随包那份 —— 缓存是运行时更新过的。
type Cache struct {
	// mu 串行化清单的「读-改-写」。
	//
	// 清单是整个文件一份：两个 goroutine 各自读一遍再各自写回，
	// 后写的会把先写的条目盖掉。面板下载与版本探测会并发走到这里。
	mu sync.Mutex

	// Dir 是可写缓存目录。
	Dir string
	// LocalDirs 是只读候选目录。
	LocalDirs []string
}

// Archive 是一份已经就位的本地归档。
type Archive struct {
	// Path 是归档文件路径。
	Path string
	// Version 是它对应的上游版本号。
	Version string
	// Fetched 表示这一次是重新下载的，而不是直接复用本地那份。
	Fetched bool
	// FromCache 表示用的是缓存目录里的副本，而不是随包分发的那份。
	FromCache bool
	// Stale 表示没能确认上游版本（断网 / GitHub 被墙），沿用了本地那份。
	Stale bool
}

// Ensure 确保某个面板有一份可用的本地归档。
//
// 规则和 fetch_mihomo 一致，只是多了一层版本判断：
//   - 本地有且版本就是上游最新 → 直接复用，不碰网络；
//   - 本地没有，或上游出了新版本 → 重新下载；
//   - 上游探测不通（断网、GitHub 被墙）→ 沿用本地那份，不让面板因此不可用。
func (c *Cache) Ensure(dash Dashboard, opts DownloadOptions) (Archive, error) {
	path, meta, fromCache := c.findLocal(dash)

	// 刚探测过就别再问一遍上游。设置与订阅变化都会触发面板流程，
	// 没有这个间隔的话，一晚上能问几十次。
	if path != "" && opts.CheckInterval > 0 && meta.CheckedAt != "" {
		if since, err := time.Parse(time.RFC3339, meta.CheckedAt); err == nil &&
			time.Since(since) < opts.CheckInterval {
			return Archive{Path: path, Version: meta.Version, FromCache: fromCache}, nil
		}
	}

	remote, err := probeVersion(dash, opts)
	if err != nil {
		if path == "" {
			return Archive{}, fmt.Errorf("探测 %s 的最新版本失败，且本地没有副本: %w", dash.Name, err)
		}
		// 探测不通不该让面板不可用：本地有一份就先用着，
		// 等下次能连上上游了再更新。
		return Archive{Path: path, Version: meta.Version, FromCache: fromCache, Stale: true}, nil
	}

	if path != "" && remote != "" && meta.Version == remote {
		// 本地就是最新版，复用。
		c.recordChecked(dash, remote)
		return Archive{Path: path, Version: meta.Version, FromCache: fromCache}, nil
	}

	// 本地没有，或者上游出了新版本：重新下载。
	newPath, newMeta, err := c.downloadArchive(dash, opts)
	if err != nil {
		if path != "" {
			// 下载失败但本地还有能用的，继续用旧的，别把面板弄没。
			return Archive{Path: path, Version: meta.Version, FromCache: fromCache}, nil
		}
		return Archive{}, err
	}
	newMeta.Version = remote
	now := time.Now().Format(time.RFC3339)
	newMeta.FetchedAt = now
	newMeta.CheckedAt = now
	c.saveMeta(c.Dir, dash.ID, newMeta)
	return Archive{Path: newPath, Version: remote, Fetched: true, FromCache: true}, nil
}

// findLocal 在本地找一份可用的归档。
//
// 缓存目录优先：运行时更新过的副本比随包分发的那份新。
func (c *Cache) findLocal(dash Dashboard) (string, ArchiveMeta, bool) {
	checked := c.readManifest(c.Dir).Dashboards[dash.ID].CheckedAt

	if path, meta, ok := c.lookup(c.Dir, dash); ok {
		meta.CheckedAt = checked
		return path, meta, true
	}
	for _, dir := range c.LocalDirs {
		if path, meta, ok := c.lookup(dir, dash); ok {
			meta.CheckedAt = checked
			return path, meta, false
		}
	}
	return "", ArchiveMeta{}, false
}

// lookup 在某个目录里找面板归档，并读同目录清单里的版本信息。
func (c *Cache) lookup(dir string, dash Dashboard) (string, ArchiveMeta, bool) {
	if dir == "" {
		return "", ArchiveMeta{}, false
	}
	path := filepath.Join(dir, dash.Archive)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", ArchiveMeta{}, false
	}
	meta := c.readManifest(dir).Dashboards[dash.ID]
	if meta.Size == 0 {
		meta.Size = info.Size()
	}
	return path, meta, true
}

// readManifest 读一个目录里的清单。文件不存在或坏了都当成空清单。
//
// 返回的清单里 map 一定非空：调用方会直接往里写。
func (c *Cache) readManifest(dir string) Manifest {
	empty := Manifest{Dashboards: map[string]ArchiveMeta{}}
	if dir == "" {
		return empty
	}
	b, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err != nil {
		return empty
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return empty
	}
	if m.Dashboards == nil {
		m.Dashboards = map[string]ArchiveMeta{}
	}
	return m
}

// saveMeta 把一条记录写进目录里的清单。
func (c *Cache) saveMeta(dir string, id string, meta ArchiveMeta) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.saveMetaLocked(dir, id, meta)
}

// saveMetaLocked 是 saveMeta 的不加锁版本。
//
// 读-改-写必须在同一把锁里完成，否则并发写会互相覆盖条目。
func (c *Cache) saveMetaLocked(dir string, id string, meta ArchiveMeta) {
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	m := c.readManifest(dir)
	m.Dashboards[id] = meta
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	_ = safeio.WriteFile(filepath.Join(dir, ManifestName), b, 0o644)
}

// recordChecked 记下「刚刚确认过上游版本」。
//
// 只更新探测时间，不动版本号：随包那份的版本号写在它自己的清单里，
// 这里记错了会让下次比对时把旧版本当成新的。
func (c *Cache) recordChecked(dash Dashboard, version string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	m := c.readManifest(c.Dir)
	entry := m.Dashboards[dash.ID]
	entry.CheckedAt = time.Now().Format(time.RFC3339)
	m.Dashboards[dash.ID] = entry
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return
	}
	_ = safeio.WriteFile(filepath.Join(c.Dir, ManifestName), b, 0o644)
}

// downloadArchive 从 GitHub 下载面板归档到缓存目录。
//
// 先写 .part 再改名：中断时不会留下半个损坏的归档，
// 而一个损坏的归档会被当成「本地已有」一直用下去。
func (c *Cache) downloadArchive(dash Dashboard, opts DownloadOptions) (string, ArchiveMeta, error) {
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return "", ArchiveMeta{}, err
	}
	out := filepath.Join(c.Dir, dash.Archive)
	// 每次下载各用各的文件名：面板下载可能在后台刷新与手动安装时同时发生，
	// 共用一个固定的 .part 会把归档写花 ——
	// 而一个写花的归档会被当成「本地已有」一直用下去。
	part, cerr := os.CreateTemp(c.Dir, dash.Archive+".part.*")
	if cerr != nil {
		return "", ArchiveMeta{}, cerr
	}
	tmp := part.Name()
	part.Close()
	// fetchArchive 只截断不改权限，这里先给回原来的 0644。
	if cerr := os.Chmod(tmp, 0o644); cerr != nil {
		return "", ArchiveMeta{}, cerr
	}

	attempts := opts.Attempts
	if attempts <= 0 {
		attempts = 2
	}
	clients := buildClients(opts)

	var lastErr error
	for i := 0; i < attempts; i++ {
		for _, client := range clients {
			meta, err := fetchArchive(client, dash.URL, tmp)
			if err == nil {
				if err := safeio.Rename(tmp, out); err != nil {
					os.Remove(tmp)
					return "", ArchiveMeta{}, err
				}
				return out, meta, nil
			}
			lastErr = err
			os.Remove(tmp)
		}
	}
	return "", ArchiveMeta{}, fmt.Errorf("下载 %s 失败: %w", dash.URL, lastErr)
}

// fetchArchive 下载一个归档到临时文件，同时算出大小与摘要。
func fetchArchive(client *http.Client, url string, dst string) (ArchiveMeta, error) {
	var meta ArchiveMeta
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return meta, err
	}
	req.Header.Set("User-Agent", "fnOS-Mihomo")

	resp, err := client.Do(req)
	if err != nil {
		return meta, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return meta, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	f, err := os.Create(dst)
	if err != nil {
		return meta, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), resp.Body)
	closeErr := f.Close()
	if err != nil {
		os.Remove(dst)
		return meta, err
	}
	if closeErr != nil {
		os.Remove(dst)
		return meta, closeErr
	}

	meta.Size = n
	meta.SHA256 = hex.EncodeToString(h.Sum(nil))
	meta.ETag = strings.Trim(resp.Header.Get("ETag"), `"`)
	return meta, nil
}

// probeVersion 探测上游的最新版本号。
//
// 先试代理再试直连：NAS 上直连 GitHub 常常被墙，走节点反而能通。
func probeVersion(dash Dashboard, opts DownloadOptions) (string, error) {
	var lastErr error
	for _, client := range buildClients(opts) {
		v, err := probeVersionOnce(client, dash)
		if err == nil {
			return v, nil
		}
		lastErr = err
	}
	return "", lastErr
}

// probeVersionOnce 按面板指定的方式取一次上游版本号。
func probeVersionOnce(client *http.Client, dash Dashboard) (string, error) {
	switch dash.VersionKind {
	case VersionFromRedirect:
		// 不能跟随重定向：我们要的就是 Location 里的那个 tag。
		// 复用同一个 Transport，只换重定向策略。
		noFollow := &http.Client{
			Transport: client.Transport,
			Timeout:   client.Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		req, err := http.NewRequest(http.MethodGet, dash.VersionURL, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("User-Agent", "fnOS-Mihomo")
		resp, err := noFollow.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		loc := resp.Header.Get("Location")
		if loc == "" {
			return "", fmt.Errorf("%s 没有返回重定向地址", dash.VersionURL)
		}
		return tagFromLocation(loc)
	case VersionFromAtom:
		req, err := http.NewRequest(http.MethodGet, dash.VersionURL, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("User-Agent", "fnOS-Mihomo")
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		if err != nil {
			return "", err
		}
		m := commitPattern.FindSubmatch(body)
		if m == nil {
			return "", fmt.Errorf("没从 %s 里解析出提交号", dash.VersionURL)
		}
		return string(m[1]), nil
	default:
		return "", fmt.Errorf("未知的版本探测方式: %d", dash.VersionKind)
	}
}

// tagFromLocation 从重定向地址里取出 tag。
//
// 形如 https://github.com/MetaCubeX/metacubexd/releases/tag/v1.276.0，
// 版本号就是最后一段。
func tagFromLocation(loc string) (string, error) {
	u, err := url.Parse(loc)
	if err != nil {
		return "", err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 {
		return "", fmt.Errorf("重定向地址里没有版本号: %s", loc)
	}
	tag := parts[len(parts)-1]
	if tag == "" {
		return "", fmt.Errorf("重定向地址里没有版本号: %s", loc)
	}
	return tag, nil
}
