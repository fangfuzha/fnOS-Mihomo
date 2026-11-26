package deploy

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// cmdHotUpdate 热更新面板二进制与前端资源。
//
// 内核不动、订阅不动，只换面板程序和前端文件 —— 改一行 CSS 不用重装包。
//
// **启停必须走 fnOS 自己的应用管理器（appcenter-cli），不能自己拉进程。**
//
// 之前那版是用 pkill + setsid 自己拉起来的，后果在真 NAS 上实测过：
//
//   - 飞牛桌面里应用变成「禁用」。fnOS 只认自己启动的进程，
//     我们 setsid 起的那个它完全不知道，进程一死它就认为应用挂了；
//   - 那个野进程还占着 8765。用户点「启用」时 fnOS 先做端口预检，
//     于是报「无法启用，端口被占用」，应用再也启不动；
//   - pid 文件也没写，应用自己的 status 判断同样是错的。
//
// 走 appcenter-cli stop/start 就没这些问题：状态、pid 文件、端口占用
// 全都是 fnOS 自己在管，跟桌面里显示的一直一致。
func cmdHotUpdate(args []string) error {
	fs := flag.NewFlagSet("hot-update", flag.ExitOnError)
	common := addCommon(fs)
	skipWeb := fs.Bool("skip-web", false, "只更新面板二进制")
	noRestart := fs.Bool("no-restart", false, "只上传，不替换也不重启")
	if err := fs.Parse(args); err != nil {
		return err
	}

	env, err := resolveEnv(common.envFile)
	if err != nil {
		return err
	}

	panel := env.PanelBin
	if _, err := os.Stat(panel); err != nil {
		return fmt.Errorf("找不到面板二进制 %s（先跑 go run ./scripts build）", panel)
	}
	webIndex := filepath.Join(env.WebDir, "index.html")
	webCSS := filepath.Join(env.WebDir, "app.css")
	webJS := filepath.Join(env.WebDir, "app.js")
	if !*skipWeb {
		// 样式也要一起换：只换 index.html 的话，
		// 新页面引用的 /app.css 在机器上不存在，
		// 静态服务会拿 index.html 当 CSS 回落回去，页面就没样式了。
		for _, f := range []string{webIndex, webCSS, webJS} {
			if _, err := os.Stat(f); err != nil {
				return fmt.Errorf("找不到前端文件 %s", f)
			}
		}
	}

	c, err := dial(env, common.insecure)
	if err != nil {
		return err
	}
	defer c.Close()

	panelTmp := remoteJoin(env.RemoteTmp, "panel.new")
	if err := c.Put(panel, panelTmp); err != nil {
		return err
	}
	fmt.Println("已上传 " + panel)
	if !*skipWeb {
		if err := c.Put(webIndex, remoteJoin(env.RemoteTmp, "index.html.new")); err != nil {
			return err
		}
		if err := c.Put(webCSS, remoteJoin(env.RemoteTmp, "app.css.new")); err != nil {
			return err
		}
		if err := c.Put(webJS, remoteJoin(env.RemoteTmp, "app.js.new")); err != nil {
			return err
		}
		fmt.Println("已上传前端 " + webIndex + " / " + webCSS + " / " + webJS)
	}
	if *noRestart {
		fmt.Println("--no-restart：只上传，未替换也未重启")
		return nil
	}

	var webCmds string
	if !*skipWeb {
		webCmds = strings.Join([]string{
			fmt.Sprintf("cp %s $APP/www/index.html", shQuote(remoteJoin(env.RemoteTmp, "index.html.new"))),
			fmt.Sprintf("cp %s $APP/www/app.css", shQuote(remoteJoin(env.RemoteTmp, "app.css.new"))),
			fmt.Sprintf("cp %s $APP/www/app.js", shQuote(remoteJoin(env.RemoteTmp, "app.js.new"))),
		}, "\n")
	}

	// 整段都用 sudo 跑：appcenter-cli 要连 rpcbroker 的 unix socket，
	// 普通用户没权限；应用目录下的文件也写不进去。
	script := strings.Join([]string{
		fmt.Sprintf("APP=%s", env.AppDir),
		fmt.Sprintf("NAME=%s", env.AppName),
		// 先用 fnOS 自己的方式停：它会把应用状态记成已停止，
		// 桌面里的显示也跟着走。
		"appcenter-cli stop \"$NAME\" >/dev/null 2>&1 || true",
		"sleep 3",
		// 停完还活着的进程就是野进程（fnOS 不认它们），
		// 不收掉的话端口腾不出来，fnOS 启应用时照样报端口被占用。
		"pkill -f \"$APP/bin/panel\" || true",
		"pkill -f \"$APP/bin/mihomo\" || true",
		"sleep 1",
		"pkill -9 -f \"$APP/bin/panel\" 2>/dev/null || true",
		"pkill -9 -f \"$APP/bin/mihomo\" 2>/dev/null || true",
		// 换文件。
		fmt.Sprintf("cp %s $APP/bin/panel", shQuote(panelTmp)),
		"chmod +x $APP/bin/panel",
		webCmds,
		"rm -f " + shQuote(panelTmp),
		// 再用 fnOS 启：状态、pid 文件、端口占用都归它管。
		"appcenter-cli start \"$NAME\"",
		"sleep 6",
		"echo \"--- 状态 ---\"",
		"appcenter-cli status \"$NAME\"",
		"echo \"--- 进程 ---\"",
		"ps -eo pid,ppid,args | grep -E \"bin/(panel|mihomo)\" | grep -v grep",
		"echo \"--- 面板接口 ---\"",
		fmt.Sprintf("curl -s -m 5 -o /dev/null -w 'http=%%{http_code}\\n' http://127.0.0.1:%d/api/status", env.PanelPort),
	}, "\n")

	out, err := c.Run(script, true, 240*time.Second)
	fmt.Print(out)
	return err
}
