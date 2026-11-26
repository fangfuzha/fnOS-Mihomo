package deploy

import (
	"flag"
	"fmt"
	"strings"
	"time"
)

// cmdVerify 检查 NAS 上的运行状态。
//
// 这一段对应原来 round_dash.py 里的检查部分：面板状态、内核控制口、
// 面板目录、配置里的 external-ui、日志尾部与进程表。
// 检查都是只读的，不会改 NAS 上的任何东西。
func cmdVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	common := addCommon(fs)
	wait := fs.Int("wait", 0, "等待第三方面板下载完成的秒数（0 表示不等）")
	if err := fs.Parse(args); err != nil {
		return err
	}

	env, err := resolveEnv(common.envFile)
	if err != nil {
		return err
	}
	c, err := dial(env, common.insecure)
	if err != nil {
		return err
	}
	defer c.Close()

	// 都用 127.0.0.1：面板和内核都在 NAS 本机，不必绕网络。
	checks := []struct {
		name string
		cmd  string
	}{
		{"/api/status", fmt.Sprintf("curl -s -m 10 http://127.0.0.1:%d/api/status", env.PanelPort)},
		{"/api/dashboard", fmt.Sprintf("curl -s -m 10 http://127.0.0.1:%d/api/dashboard", env.PanelPort)},
		{"内核控制口 /ui/", fmt.Sprintf("curl -s -m 8 -o /dev/null -w %s http://127.0.0.1:%d/ui/", shQuote("ui=%{http_code}"), env.ControllerPort)},
		{"面板目录", fmt.Sprintf("ls %s/ui 2>/dev/null | head -20; echo %s; cat %s/.dashboard 2>/dev/null", env.AppDataDir, shQuote("--- marker:"), env.AppDataDir)},
		{"config.yaml external-ui", fmt.Sprintf("grep -n %s %s/config.yaml 2>/dev/null", shQuote("external-ui"), env.AppDataDir)},
		{"面板日志尾部", fmt.Sprintf("tail -30 %s/mihomo-panel.log 2>/dev/null", env.AppDataDir)},
		{"进程", "ps -eo pid,ppid,args | grep -E " + shQuote("bin/(panel|mihomo)") + " | grep -v grep"},
	}

	// 面板刚换过时要等它把第三方面板下完。
	if *wait > 0 {
		fmt.Printf("=== 等待第三方面板（最多 %d 秒）===\n", *wait)
		deadline := time.Now().Add(time.Duration(*wait) * time.Second)
		for time.Now().Before(deadline) {
			out, err := c.Run(fmt.Sprintf("curl -s -m 10 http://127.0.0.1:%d/api/dashboard", env.PanelPort), false, 30*time.Second)
			if err == nil {
				fmt.Print(out + "\n")
				if strings.Contains(out, "\"status\":\"downloading\"") {
					time.Sleep(5 * time.Second)
					continue
				}
				break
			}
			time.Sleep(5 * time.Second)
		}
	}

	for _, ck := range checks {
		fmt.Println("=== " + ck.name + " ===")
		out, err := c.Run(ck.cmd, false, 60*time.Second)
		fmt.Print(out)
		if err != nil {
			fmt.Println("(执行失败: " + err.Error() + ")")
		}
	}
	return nil
}
