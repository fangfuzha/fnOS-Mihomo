package deploy

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// defaultEnvPath 返回默认的参数文件路径：项目根目录下的 .env。
//
// dotenv 的约定就是这样 —— .env 放项目根，模板是 .env.example。
//
// 查找顺序：
//  1. 可执行文件所在目录往上找（正常构建的情况）；
//  2. 当前工作目录往上找（go run . 的情况）。
func defaultEnvPath() string {
	if exe, err := os.Executable(); err == nil {
		if p := findEnvFile(filepath.Dir(exe)); p != "" {
			return p
		}
	}
	if wd, err := os.Getwd(); err == nil {
		if p := findEnvFile(wd); p != "" {
			return p
		}
	}
	return ".env"
}

// findEnvFile 从 start 往上找项目根，返回那里的 .env。
func findEnvFile(start string) string {
	dir := start
	for i := 0; i < 8; i++ {
		if isProjectRoot(dir) {
			// 根目录认出来了就返回它的 .env：
			// 文件还没有时报错信息也能指向正确的位置。
			return filepath.Join(dir, ".env")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// isProjectRoot 判断目录是不是项目根：
// 根目录的 go.mod 里 module 正好是 fnos-mihomo。
//
// 必须精确匹配：根目录的 module 是 fnos-mihomo，
// 光看「这里有 go.mod」会把别的模块也当成根。
func isProjectRoot(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")) == "fnos-mihomo"
		}
	}
	return false
}

// Env 是部署工具用到的全部参数。
type Env struct {
	// NAS 连接
	Host       string
	Port       int
	User       string
	Pass       string
	SudoPass   string
	KeyFile    string
	KnownHosts string

	// NAS 上的安装位置
	AppName        string
	AppDir         string
	AppDataDir     string
	EtcDir         string
	PanelPort      int
	ControllerPort int
	RemoteTmp      string

	// 本地路径（已解析成绝对路径）
	RepoRoot string
	PanelBin string
	WebDir   string
	FpkFile  string
}

// resolveEnv 读参数文件并合成最终配置。
//
// 优先级：进程环境变量 > 参数文件。
// 进程环境变量优先是方便临时覆盖（例如只换一次 NAS_HOST 试试）。
func resolveEnv(path string) (*Env, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	raw, err := parseEnvFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("找不到参数文件 %s（模板见项目根目录的 .env.example，先复制一份）", path)
		}
		return nil, err
	}

	// 普通参数：允许引用别的键。
	get := func(key, def string) string {
		if v, ok := raw[key]; ok && v != "" {
			return expandVars(v, raw)
		}
		if v := os.Getenv(key); v != "" {
			return v
		}
		return def
	}
	// 密码这类值不展开：密码里本来就可能有 $ 和引号，展开只会把它改坏。
	getRaw := func(key, def string) string {
		if v, ok := raw[key]; ok && v != "" {
			return v
		}
		if v := os.Getenv(key); v != "" {
			return v
		}
		return def
	}

	e := &Env{
		Host:       get("NAS_HOST", ""),
		Port:       atoi(get("NAS_PORT", "22"), 22),
		User:       get("NAS_USER", ""),
		Pass:       getRaw("NAS_PASS", ""),
		SudoPass:   getRaw("NAS_SUDO_PASS", ""),
		KeyFile:    get("NAS_KEY_FILE", ""),
		KnownHosts: get("NAS_KNOWN_HOSTS", ""),

		AppName:        get("APP_NAME", "Mihomo"),
		AppDir:         get("APP_DIR", "/vol1/@appcenter/Mihomo"),
		AppDataDir:     get("APPDATA_DIR", "/vol1/@appdata/Mihomo"),
		EtcDir:         get("ETC_DIR", "/var/apps/Mihomo/etc"),
		PanelPort:      atoi(get("PANEL_PORT", "8765"), 8765),
		ControllerPort: atoi(get("CONTROLLER_PORT", "9090"), 9090),
		RemoteTmp:      get("REMOTE_TMP", "/tmp"),
	}

	if e.SudoPass == "" {
		e.SudoPass = e.Pass
	}
	if e.Host == "" {
		return nil, fmt.Errorf("参数里缺少 NAS_HOST")
	}
	if e.User == "" {
		return nil, fmt.Errorf("参数里缺少 NAS_USER")
	}
	if e.Pass == "" && e.KeyFile == "" {
		return nil, fmt.Errorf("NAS_PASS 与 NAS_KEY_FILE 至少要有一个")
	}

	// REPO_ROOT 默认就是参数文件所在目录（项目根）。
	// 本地路径都相对它解析，换机器只需要改这一处。
	repoRoot := get("REPO_ROOT", "")
	if repoRoot == "" {
		repoRoot = filepath.Dir(abs)
	}
	if !filepath.IsAbs(repoRoot) {
		repoRoot = filepath.Join(filepath.Dir(abs), repoRoot)
	}
	e.RepoRoot = filepath.Clean(repoRoot)

	e.PanelBin = e.local(get("PANEL_BIN", "build/amd64/app/bin/panel"))
	e.WebDir = e.local(get("WEB_DIR", "web"))
	e.FpkFile = e.local(get("FPK_FILE", "Mihomo-x86.fpk"))

	if e.KnownHosts != "" && !filepath.IsAbs(e.KnownHosts) {
		e.KnownHosts = filepath.Join(filepath.Dir(abs), e.KnownHosts)
	}
	if e.KeyFile != "" && !filepath.IsAbs(e.KeyFile) {
		e.KeyFile = e.local(e.KeyFile)
	}
	return e, nil
}

// local 把相对路径按仓库根解析。
func (e *Env) local(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(e.RepoRoot, p)
}

// parseEnvFile 解析 KEY=VALUE 形式的参数文件。
//
// 只认三种写法：整行注释（# 开头）、KEY=VALUE、export KEY=VALUE。
// 值外面的成对引号会被去掉，方便写带空格的路径。
func parseEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	m := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		if key != "" {
			m[key] = val
		}
	}
	return m, sc.Err()
}

// expandVars 展开花括号形式的变量引用。
//
// 只处理带花括号的形式：裸 $ 在密码和正则里太常见，碰不得。
// 引用先查参数文件，再查进程环境。
func expandVars(v string, m map[string]string) string {
	for i := 0; i < 8; i++ {
		start := strings.Index(v, "${")
		if start < 0 {
			return v
		}
		end := strings.Index(v[start:], "}")
		if end < 0 {
			return v
		}
		end += start
		name := v[start+2 : end]
		val, ok := m[name]
		if !ok {
			val = os.Getenv(name)
		}
		v = v[:start] + val + v[end+1:]
	}
	return v
}

// atoi 解析整数，失败时用默认值。
func atoi(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return n
}
