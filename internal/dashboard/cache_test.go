package dashboard

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeServer 假装 GitHub：/version 返回重定向到 tag，/dl 返回归档内容。
type fakeServer struct {
	tag       string
	versionOK bool
	downloads int32
}

func (f *fakeServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			if !f.versionOK {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("Location", "https://example.com/releases/tag/"+f.tag)
			w.WriteHeader(http.StatusFound)
		case "/dl":
			n := atomic.AddInt32(&f.downloads, 1)
			// 每次下载给不同的内容：两份下载写进同一个文件时能看出来。
			w.Write([]byte(fmt.Sprintf("fake-archive-%d", n)))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeServer) downloadCount() int {
	return int(atomic.LoadInt32(&f.downloads))
}

// testDashboard 造一个指向假服务器的面板定义。
func testDashboard(srv *httptest.Server) Dashboard {
	return Dashboard{
		ID:          "test",
		Name:        "测试面板",
		URL:         srv.URL + "/dl",
		ClientPath:  "/ui/",
		Archive:     "test.zip",
		VersionURL:  srv.URL + "/version",
		VersionKind: VersionFromRedirect,
	}
}

// writeLocalArchive 在目录里放一份归档，并在同目录清单里记下版本。
func writeLocalArchive(t *testing.T, dir string, dash Dashboard, version string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, dash.Archive)
	if err := os.WriteFile(path, []byte("fake-archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &Cache{Dir: dir}
	c.saveMeta(dir, dash.ID, ArchiveMeta{Version: version, Size: int64(len("fake-archive"))})
	return path
}

// TestEnsureReusesLocalArchiveWhenVersionMatches 本地这份就是上游最新版时，
// 直接复用，一个字节都不用重新下。
func TestEnsureReusesLocalArchiveWhenVersionMatches(t *testing.T) {
	fs := &fakeServer{tag: "v1.0.0", versionOK: true}
	srv := fs.start(t)
	dash := testDashboard(srv)

	dir := t.TempDir()
	local := writeLocalArchive(t, dir, dash, "v1.0.0")

	c := &Cache{Dir: dir}
	a, err := c.Ensure(dash, DownloadOptions{})
	if err != nil {
		t.Fatalf("Ensure 失败: %v", err)
	}
	if a.Fetched {
		t.Error("版本一致时不该重新下载")
	}
	if a.Version != "v1.0.0" {
		t.Errorf("版本不对: %s", a.Version)
	}
	if a.Path != local {
		t.Errorf("用错了本地副本: %s", a.Path)
	}
	if n := fs.downloadCount(); n != 0 {
		t.Errorf("不该发起下载请求，实际 %d 次", n)
	}
}

// TestEnsureDownloadsWhenLocalMissing 本地没有副本时要下载。
func TestEnsureDownloadsWhenLocalMissing(t *testing.T) {
	fs := &fakeServer{tag: "v2.0.0", versionOK: true}
	srv := fs.start(t)
	dash := testDashboard(srv)

	dir := t.TempDir()
	c := &Cache{Dir: dir}
	a, err := c.Ensure(dash, DownloadOptions{})
	if err != nil {
		t.Fatalf("Ensure 失败: %v", err)
	}
	if !a.Fetched {
		t.Error("本地没有时应该下载")
	}
	if a.Version != "v2.0.0" {
		t.Errorf("版本不对: %s", a.Version)
	}
	if _, err := os.Stat(filepath.Join(dir, dash.Archive)); err != nil {
		t.Errorf("归档没落到缓存目录: %v", err)
	}
	// 下载后清单里要记下版本，下次才能判断「本地就是最新」。
	meta := c.readManifest(dir).Dashboards[dash.ID]
	if meta.Version != "v2.0.0" {
		t.Errorf("清单里的版本不对: %s", meta.Version)
	}
	if meta.SHA256 == "" {
		t.Error("清单里应该记下摘要")
	}
	if n := fs.downloadCount(); n != 1 {
		t.Errorf("下载次数不对，实际 %d 次", n)
	}
}

// TestEnsureRefreshesWhenUpstreamHasNewVersion 上游出了新版本要重新下载。
func TestEnsureRefreshesWhenUpstreamHasNewVersion(t *testing.T) {
	fs := &fakeServer{tag: "v2.0.0", versionOK: true}
	srv := fs.start(t)
	dash := testDashboard(srv)

	dir := t.TempDir()
	writeLocalArchive(t, dir, dash, "v1.0.0")

	c := &Cache{Dir: dir}
	a, err := c.Ensure(dash, DownloadOptions{})
	if err != nil {
		t.Fatalf("Ensure 失败: %v", err)
	}
	if !a.Fetched {
		t.Error("上游有新版时应该重新下载")
	}
	if a.Version != "v2.0.0" {
		t.Errorf("版本不对: %s", a.Version)
	}
	if n := fs.downloadCount(); n != 1 {
		t.Errorf("下载次数不对，实际 %d 次", n)
	}
}

// TestEnsureKeepsLocalWhenUpstreamUnreachable 上游探测不通时沿用本地副本，
// 不能让面板因为连不上 GitHub 就用不了。
func TestEnsureKeepsLocalWhenUpstreamUnreachable(t *testing.T) {
	fs := &fakeServer{versionOK: false}
	srv := fs.start(t)
	dash := testDashboard(srv)

	dir := t.TempDir()
	local := writeLocalArchive(t, dir, dash, "v1.0.0")

	c := &Cache{Dir: dir}
	a, err := c.Ensure(dash, DownloadOptions{})
	if err != nil {
		t.Fatalf("Ensure 失败: %v", err)
	}
	if a.Fetched {
		t.Error("探测不通时不该下载")
	}
	if !a.Stale {
		t.Error("应该标记出「沿用本地副本」")
	}
	if a.Path != local {
		t.Errorf("应该沿用本地副本，实际 %s", a.Path)
	}
}

// TestEnsureFailsWhenNothingLocalAndUnreachable 本地没有、上游也不通，
// 只能报错 —— 没东西可用。
func TestEnsureFailsWhenNothingLocalAndUnreachable(t *testing.T) {
	fs := &fakeServer{versionOK: false}
	srv := fs.start(t)
	dash := testDashboard(srv)

	c := &Cache{Dir: t.TempDir()}
	if _, err := c.Ensure(dash, DownloadOptions{}); err == nil {
		t.Fatal("期望报错，实际没有")
	}
}

// TestEnsurePrefersCacheOverPackaged 缓存目录里的副本优先于随包分发的那份。
func TestEnsurePrefersCacheOverPackaged(t *testing.T) {
	fs := &fakeServer{tag: "v1.0.0", versionOK: true}
	srv := fs.start(t)
	dash := testDashboard(srv)

	cacheDir := t.TempDir()
	packDir := t.TempDir()
	writeLocalArchive(t, cacheDir, dash, "v1.0.0")
	packaged := writeLocalArchive(t, packDir, dash, "v1.0.0")

	c := &Cache{Dir: cacheDir, LocalDirs: []string{packDir}}
	a, err := c.Ensure(dash, DownloadOptions{})
	if err != nil {
		t.Fatalf("Ensure 失败: %v", err)
	}
	if a.Path == packaged {
		t.Error("应该用缓存目录里的那份")
	}
	if !a.FromCache {
		t.Error("FromCache 应该为真")
	}
}

// TestEnsureSkipsProbeWithinCheckInterval 刚探测过就不该再问上游。
func TestEnsureSkipsProbeWithinCheckInterval(t *testing.T) {
	fs := &fakeServer{tag: "v1.0.0", versionOK: true}
	srv := fs.start(t)
	dash := testDashboard(srv)

	dir := t.TempDir()
	writeLocalArchive(t, dir, dash, "v1.0.0")

	c := &Cache{Dir: dir}
	opts := DownloadOptions{CheckInterval: 6 * 60 * 60 * 1e9}
	if _, err := c.Ensure(dash, opts); err != nil {
		t.Fatalf("首次 Ensure 失败: %v", err)
	}
	// 再来一次：探测间隔没到，直接用本地那份。
	a, err := c.Ensure(dash, opts)
	if err != nil {
		t.Fatalf("二次 Ensure 失败: %v", err)
	}
	if a.Fetched {
		t.Error("探测间隔内不该下载")
	}
	if n := fs.downloadCount(); n != 0 {
		t.Errorf("不该发起下载请求，实际 %d 次", n)
	}
}

// TestEnsureConcurrentDownloads 并发下载同一份面板，归档不能写花。
//
// 下载先写 .part 再改名。共用一个固定的 .part 时两次下载会写进同一个文件，
// 落地的是两份内容拼起来 —— 一个写花的归档会被当成「本地已有」一直用下去。
func TestEnsureConcurrentDownloads(t *testing.T) {
	fs := &fakeServer{tag: "v1.0.0", versionOK: true}
	srv := fs.start(t)
	dash := testDashboard(srv)

	dir := t.TempDir()
	c := &Cache{Dir: dir}

	const n = 4
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Ensure(dash, DownloadOptions{}); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("并发下载失败: %v", err)
	}

	// 归档是完整的一份，不能是两份拼起来的。
	b, err := os.ReadFile(filepath.Join(dir, dash.Archive))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^fake-archive-\d+$`).Match(b) {
		t.Errorf("归档内容坏了: %q", string(b))
	}

	// 不能留下 .part 残留。
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != dash.Archive && e.Name() != ManifestName {
			t.Errorf("残留了文件: %s", e.Name())
		}
	}
}

// TestSaveMetaConcurrent 并发写清单：每条记录都得在。
//
// 清单是整文件一份的「读-改-写」，并发写会互相覆盖条目 ——
// 后写的把先写的盖掉，条目就少了。
func TestSaveMetaConcurrent(t *testing.T) {
	dir := t.TempDir()
	c := &Cache{Dir: dir}

	const n = 12
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c.saveMeta(dir, fmt.Sprintf("dash-%d", i), ArchiveMeta{Version: "v1.0.0"})
		}(i)
	}
	wg.Wait()

	m := c.readManifest(dir)
	if len(m.Dashboards) != n {
		t.Errorf("清单条目数不对: %d，期望 %d", len(m.Dashboards), n)
	}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("dash-%d", i)
		if _, ok := m.Dashboards[id]; !ok {
			t.Errorf("条目 %s 丢了", id)
		}
	}

	// 不能留下临时文件。
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != ManifestName {
			t.Errorf("残留了临时文件: %s", e.Name())
		}
	}
}

// TestTagFromLocation 从重定向地址里取 tag。
func TestTagFromLocation(t *testing.T) {
	cases := []struct {
		loc  string
		want string
	}{
		{"https://github.com/MetaCubeX/metacubexd/releases/tag/v1.276.0", "v1.276.0"},
		{"https://github.com/Zephyruso/zashboard/releases/tag/v3.30.0", "v3.30.0"},
	}
	for _, tc := range cases {
		got, err := tagFromLocation(tc.loc)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", tc.loc, err)
		}
		if got != tc.want {
			t.Errorf("tagFromLocation(%s) = %s, 期望 %s", tc.loc, got, tc.want)
		}
	}
}
