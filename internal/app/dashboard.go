package app

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fnos-mihomo/internal/dashboard"
	"fnos-mihomo/internal/safeio"
)

// dashboardMarker 记录当前生效的面板标识与版本。
//
// 放在数据目录下，重启后仍然知道上次装的是哪个面板的哪一版，
// 避免每次启动都把面板目录清空、重新安装一遍。
// 格式是「<面板ID> <版本号>」，老版本只写了 ID 的标记会被当成不匹配，重新装一次即可。
const dashboardMarker = ".dashboard"

// dashboardState 记录面板下载的状态，供界面提示。
//
// want 是用户当前选中的面板；busy 表示有下载正在进行；
// err 记录上一次下载失败的原因。
type dashboardState struct {
	mu   sync.Mutex
	want string
	busy bool
	err  string
}

// DashboardStatus 返回面板状态与错误信息。
//
// 状态：ok 正常、downloading 正在下载、error 下载失败。
func (s *Service) DashboardStatus() (string, string) {
	s.dashState.mu.Lock()
	defer s.dashState.mu.Unlock()
	switch {
	case s.dashState.err != "":
		return "error", s.dashState.err
	case s.dashState.busy:
		return "downloading", ""
	default:
		return "ok", ""
	}
}

// EnsureDashboard 确保选中的第三方面板已安装到 <数据目录>/ui。
//
// 归档优先用本地那份（随包分发的，或之前缓存下来的），
// 只有本地没有、或上游出了新版本时才去 GitHub 拉。
// 安装成功后内核通过 external-ui 把这个目录托管在控制端口的 /ui/ 下。
//
// 需要下载时放到后台执行：首次安装要十几秒，不能挡住面板启动。
// busy 标记是同步置上的，界面立刻就能看到「正在下载」。
func (s *Service) EnsureDashboard(dashID string, proxyURL string) {
	s.dashState.mu.Lock()
	s.dashState.want = dashID
	if s.dashState.busy {
		// 已经在下了。等这一轮结束时会按最新的 want 再来一次。
		s.dashState.mu.Unlock()
		return
	}
	s.dashState.busy = true
	s.dashState.err = ""
	s.dashState.mu.Unlock()

	go s.downloadDashboard(dashID, proxyURL)
}

// downloadDashboard 把选中的面板装好，并在结束后对齐用户最新的选择。
func (s *Service) downloadDashboard(dashID string, proxyURL string) {
	defer func() {
		s.dashState.mu.Lock()
		s.dashState.busy = false
		next := ""
		if s.dashState.want != dashID {
			next = s.dashState.want
		}
		s.dashState.mu.Unlock()
		// 下载期间用户又换了面板，按新选择再来一轮。
		if next != "" {
			s.EnsureDashboard(next, proxyURL)
		}
	}()

	dest := filepath.Join(s.dataDir, "ui")
	marker := filepath.Join(s.dataDir, dashboardMarker)

	// 没有面板缓存（测试里就是这样）就不装面板 —— 没有归档可装。
	if s.dash == nil {
		return
	}

	dash, ok := dashboard.DashboardByID(dashID)
	if !ok {
		// 没选面板：清掉目录，别让内核继续托管上一份。
		_ = os.RemoveAll(dest)
		_ = safeio.WriteFile(marker, []byte(""), 0o600)
		return
	}

	opts := dashboard.DownloadOptions{
		// 本地代理：NAS 上直连 GitHub 常常被墙，走内核的代理端口更稳。
		ProxyURL: proxyURL,
		Attempts: 2,
		Timeout:  5 * time.Minute,
		// 探测上游版本不必太频繁：设置一变就会走到这里。
		CheckInterval: 6 * time.Hour,
	}

	// 先备好归档：本地有且是最新版就直接复用，
	// 本地没有或上游更新了才去 GitHub 拉。
	archive, err := s.dash.Ensure(dash, opts)
	if err != nil {
		log.Printf("准备第三方面板失败: %v", err)
		s.dashState.mu.Lock()
		s.dashState.err = err.Error()
		s.dashState.mu.Unlock()
		return
	}
	if archive.Fetched {
		log.Printf("已更新第三方面板归档: %s (%s)", dash.Name, archive.Version)
	}

	// 装的就是这一版、目录里也已经解压好了，就不用再动一次。
	want := dash.ID + " " + archive.Version
	cur, _ := os.ReadFile(marker)
	if strings.TrimSpace(string(cur)) == want && fileExists(filepath.Join(dest, "index.html")) {
		return
	}

	log.Printf("正在安装第三方面板: %s (%s)", dash.Name, archive.Version)
	if err := dashboard.InstallArchive(archive.Path, dest); err != nil {
		log.Printf("安装第三方面板失败: %v", err)
		s.dashState.mu.Lock()
		s.dashState.err = err.Error()
		s.dashState.mu.Unlock()
		return
	}
	if err := safeio.WriteFile(marker, []byte(want), 0o600); err != nil {
		log.Printf("写入面板标记失败: %v", err)
	}
	s.dashState.mu.Lock()
	s.dashState.err = ""
	s.dashState.mu.Unlock()
	log.Printf("第三方面板已就绪: %s", dash.Name)
}

// fileExists 判断路径是否存在。
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
