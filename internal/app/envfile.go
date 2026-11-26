package app

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"fnos-mihomo/internal/settings"
)

// ApplyEnvFile 读取安装向导写下的 config.env，把其中的初始值套用到配置里。
//
// 只在首次安装时套用（config.json 还不存在）。
// 之后再启动一律以面板里保存的配置为准 —— 否则用户改过的端口、密钥
// 每次重启都会被向导里的旧值冲回去。
func ApplyEnvFile(store *settings.Store, etcDir string) {
	if etcDir == "" {
		return
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(store.Path()), "config.json")); err == nil {
		// 已经初始化过了，不再套用向导值。
		return
	}
	b, err := os.ReadFile(filepath.Join(etcDir, "config.env"))
	if err != nil {
		return
	}

	vals := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		// config.env 由 printf %q 写入：普通值原样输出，含空格或特殊字符的值
		// 会被包在单引号里（空值写作 ''）。这里按 shell 语义还原。
		parsed := parseShellValue(val)
		if parsed == "" {
			continue
		}
		vals[strings.TrimSpace(key)] = parsed
	}

	_ = store.Update(func(c *settings.Settings) {
		if v, ok := vals["MIXED_PORT"]; ok && v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				c.MixedPort = n
			}
		}
		if v, ok := vals["CONTROLLER_PORT"]; ok && v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				c.ControllerPort = n
			}
		}
		if v, ok := vals["CONTROLLER_SECRET"]; ok && v != "" {
			c.ControllerSecret = v
		}
		if v, ok := vals["MODE"]; ok && v != "" {
			c.Mode = v
		}
		if v, ok := vals["RULES_MODE"]; ok && v != "" {
			c.RulesMode = v
		}
		if v, ok := vals["ALLOW_LAN"]; ok && v != "" {
			c.AllowLan = v == "true"
		}
	})

	// 向导里填的订阅链接：如果面板里还没有任何订阅，就把它加进去。
	if url := strings.TrimSpace(vals["SUB_URL"]); url != "" {
		set := store.Get()
		if len(set.Subscriptions) == 0 {
			_ = store.Update(func(c *settings.Settings) {
				c.Subscriptions = append(c.Subscriptions, settings.Subscription{
					ID:      "sub-" + strconv.FormatInt(time.Now().UnixNano(), 10),
					Name:    "安装向导导入",
					URL:     url,
					Enabled: true,
				})
			})
		}
	}
}

// parseShellValue 还原 config.env 里的值。
//
// printf %q 的输出规则：普通字符串原样输出；含空格或特殊字符时用单引号包裹；
// 空字符串写成 ”。这里把成对的外层引号去掉即可。
func parseShellValue(raw string) string {
	v := strings.TrimSpace(raw)
	if len(v) >= 2 {
		first, last := v[0], v[len(v)-1]
		if (first == '\'' && last == '\'') || (first == '"' && last == '"') {
			return v[1 : len(v)-1]
		}
	}
	return v
}
