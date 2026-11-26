// Command panel 是 fnOS-Mihomo 的面板服务。
// 职责：管理订阅、生成 mihomo 配置、启停内核、控制 NAS 本机出站接管，并对外提供 Web 界面。
//
// 这个文件只做装配与启停：业务逻辑都在 internal/app 里，
// HTTP 处理在 internal/api 里 —— 便于各自写单测。
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"fnos-mihomo/internal/api"
	"fnos-mihomo/internal/app"
	"fnos-mihomo/internal/core"
	"fnos-mihomo/internal/dashboard"
	"fnos-mihomo/internal/gateway"
	"fnos-mihomo/internal/settings"
)

func main() {
	var (
		listenPort = flag.Int("port", 8765, "面板监听端口")
		dataDir    = flag.String("data", "", "数据目录（配置、日志、节点缓存）")
		binDir     = flag.String("bin", "", "内核二进制所在目录")
		webDir     = flag.String("web", "", "静态页面目录")
		etcDir     = flag.String("etc", "", "配置目录（安装向导写入的 config.env 所在处）")
	)
	flag.Parse()

	if *dataDir == "" {
		*dataDir = "/vol1/@appdata/Mihomo"
	}
	if *binDir == "" {
		*binDir = "/vol1/@appcenter/Mihomo"
	}

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("创建数据目录失败: %v", err)
	}

	store := settings.NewStore(filepath.Join(*dataDir, "config.json"))
	app.ApplyEnvFile(store, *etcDir)
	// 控制口默认绑在所有网卡上，密钥不能空着：
	// 空密钥等于把节点凭据摊给整个局域网。
	// 函数自己判断是不是首次运行，用户显式清空过的不会被塞回来。
	app.EnsureControllerSecret(store)

	// geodata 随应用包分发，放在 bin 目录下。
	// 国内 NAS 连不上 GitHub releases，依赖内核自动下载必然失败。
	geoDir := filepath.Join(*binDir, "bin")
	_ = store.Update(func(c *settings.Settings) {
		if c.GeoDir == "" {
			c.GeoDir = geoDir
		}
	})

	// 内核也会在工作目录里找 geodata 文件，一并复制过去当双保险。
	app.CopyGeoData(geoDir, *dataDir)

	coreMgr := core.New(
		filepath.Join(*binDir, "bin", "mihomo"),
		filepath.Join(*dataDir, "config.yaml"),
		*dataDir,
	)
	gw := gateway.New()

	// 第三方面板归档的本地副本：
	// 随包分发的那份在应用目录里（只读），运行时下载的新版本放进数据目录。
	// 两者都留着，缓存优先 —— 缓存是运行时更新过的。
	dashCache := &dashboard.Cache{
		Dir:       filepath.Join(*dataDir, "cache", "dashboards"),
		LocalDirs: []string{filepath.Join(*binDir, "bin", "dashboards")},
	}

	svc := app.NewService(store, coreMgr, *dataDir, dashCache)

	srv := api.NewServer(store, coreMgr, gw)
	srv.FetchSubscription = svc.FetchSubscription
	srv.ApplyConfig = svc.ApplyConfig
	// 是否有可用节点，以本地缓存里真实解析出的节点为准。
	// 不用订阅表里的 NodeCount：那是上次拉取时记下的数字，
	// 缓存文件丢了或被清空时它不会变，据此启动会起一个没有节点的内核。
	srv.HasNodes = svc.HasNodes
	// 面板下载状态，供界面提示用。
	srv.DashboardStatus = svc.DashboardStatus

	mux := http.NewServeMux()
	srv.Register(mux)
	mux.Handle("/", api.StaticHandler(*webDir))

	// 启动时先生成一次配置，保证内核配置与订阅一致。
	if err := svc.ApplyConfig(); err != nil {
		log.Printf("生成初始配置失败: %v", err)
	}

	// 后台刷新订阅。
	// 场景：安装向导导入的订阅首次拉取可能因为网络抖动失败，
	// 用户重启应用后应该自动补上节点，而不是干等手动点「更新」。
	go svc.RefreshSubscriptions(srv.SyncCoreState)

	// 启动后按订阅状态对齐内核：有可用节点就启动，没有就保持停止。
	// 这一步放在后台刷新之后不合适（刷新还没跑完），
	// 所以这里先按当前已缓存的节点数判断一次，
	// 刷新完成后再由 RefreshSubscriptions 重新对齐。
	srv.SyncCoreState()

	addr := fmt.Sprintf(":%d", *listenPort)
	log.Printf("面板已启动，监听 %s", addr)

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
	}

	go func() {
		if err := httpSrv.ListenAndServe(); err != nil {
			log.Printf("面板退出: %v", err)
		}
		// 面板退出时把内核与接管规则一并收干净，避免留下占端口的孤儿进程。
		_ = coreMgr.Stop()
		_ = gw.Remove()
	}()

	select {}
}
