package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// fnpackBase 是 fnpack 的官方下载地址前缀。
const fnpackBase = "https://static2.fnnas.com/fnpack"

// fnpackVersion 是固定使用的打包器版本。
//
// 不用上游最新版：1.3.x 的官方 Windows 构建有缺陷 —— 它对只读句柄调 fsync，
// 而 Windows 的 FlushFileBuffers 要求句柄可写，于是报
// "sync xxx.fpk-123: Access is denied"，在这台机器上打不出包。
// （同一个 1.3.3 的 linux-amd64 构建在 Linux 上打同样的包目录一次就过，
// 所以这是官方 Windows 构建的问题，不是我们的包结构不对，也不是杀软拦截。）
//
// 1.2.3 在 Windows 和 Linux 上都能正常出包，先统一用它。
// 要换版本：改这里的常量，并在 fnpackSHA256 里补上对应平台的摘要，
// 然后在目标平台上跑一次 fetch_fnpack —— 它会拿最小包试打，
// 打不出包就直接报错，不会把坏工具留下来。
const fnpackVersion = "1.2.3"

// fnpackStateFile 记录本地这份 fnpack 的版本与可用性。
const fnpackStateFile = "state.json"

// fnpackPlatforms 是各平台对应的官方文件名后缀。
//
// linux/arm64 对应的是 "linux-arm" 而不是 "linux-arm64" —— 后者会 404。
var fnpackPlatforms = map[string]string{
	"windows/amd64": "windows-amd64",
	"linux/amd64":   "linux-amd64",
	"linux/arm64":   "linux-arm",
	"darwin/amd64":  "darwin-amd64",
	"darwin/arm64":  "darwin-arm64",
}

// fnpackSHA256 是核对过的官方文件摘要（版本 + 平台）。
//
// 只记手上验证过的：对不上就不用这个文件。没记的平台不做校验。
var fnpackSHA256 = map[string]string{
	"1.2.3 windows/amd64": "d7af4bd716b009c58f5bcd931615f39db121e7d4b75dc759e575c4fb2879b6ee",
	"1.3.3 windows/amd64": "4f00ce8d2b503229bd109a25ff0247254a45b1b9b436f063c2f26d73ad8a8a02",
	"1.3.3 linux/amd64":   "1464d6aa9b995ebecb263c5ec60f0f4a7cbf45e0ce887836755fd65abd647d1e",
}

// fnpackState 记录本地打包器的状态。
type fnpackState struct {
	// Version 是本地这份的版本。
	Version string `json:"version"`
	// Platform 是本地这份对应的平台。
	Platform string `json:"platform"`
	// Verified 表示本地这份试打过、真能出包。
	Verified bool `json:"verified,omitempty"`
}

// cmdFetchFnpack 下载 fnpack 打包器到指定目录。
//
// 打包器是飞牛官方的第三方二进制，不进版本库：需要时自动拉取。
// 拉的永远是**当前运行平台**对应的那一份，版本固定为 fnpackVersion。
//
// 拉完要拿一个最小包试打一下，确认这个打包器在这台机器上真能出包 ——
// fnpack 打包失败时也返回 0，只有真的产出 .fpk 才算成功。
//
// 用法：fetch_fnpack [目标目录]（默认 build/fnpack）
func cmdFetchFnpack(args []string) error {
	dir := filepath.Join("build", "fnpack")
	if len(args) > 0 {
		dir = args[0]
	}
	return ensureFnpack(dir)
}

// ensureFnpack 确保指定目录里有一份可用���打包器。
//
// 打包器是飞牛官方的第三方二进制，不进版本库：需要时自动拉取。
// 拉的永远是**当前运行平台**对应的那一份，版本固定为 fnpackVersion。
func ensureFnpack(dir string) error {
	key := runtime.GOOS + "/" + runtime.GOARCH
	plat, ok := fnpackPlatforms[key]
	if !ok {
		return fmt.Errorf("官方没有 %s 对应的 fnpack 构建（只有 %s）", key, strings.Join(platformKeys(), "、"))
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	name := "fnpack"
	if runtime.GOOS == "windows" {
		name = "fnpack.exe"
	}
	out := filepath.Join(dir, name)

	state := readFnpackState(dir)
	// 本地这份就是这一版、这一平台，而且试打过能用，就别再折腾了。
	if state.Version == fnpackVersion && state.Platform == key && state.Verified && fileExists(out) {
		fmt.Printf("已是可用版本，跳过: %s (%s %s)\n", out, fnpackVersion, key)
		return nil
	}

	if err := downloadFnpack(fnpackVersion, plat, out); err != nil {
		return err
	}
	if err := validateFnpack(out); err != nil {
		return fmt.Errorf("fnpack %s 在本机打不出包: %w", fnpackVersion, err)
	}
	state = fnpackState{Version: fnpackVersion, Platform: key, Verified: true}
	if err := writeFnpackState(dir, state); err != nil {
		return err
	}
	fmt.Printf("使用 fnpack %s (%s)\n", fnpackVersion, key)
	return nil
}

// downloadFnpack 下载指定版本的打包器。
func downloadFnpack(version, plat, out string) error {
	url := fmt.Sprintf("%s/fnpack-%s-%s", fnpackBase, version, plat)
	fmt.Printf("==> 下载 fnpack %s\n", version)
	fmt.Printf("    %s\n", url)
	if err := downloadFile(url, out); err != nil {
		return err
	}
	// 下载器用 os.Create 写文件，权限是 0644：Windows 上无所谓，
	// Linux 上直接 fork/exec 会报 permission denied。
	// CI 上第一次发版就是这么失败的 —— 打包器下下来却跑不起来。
	if err := makeExecutable(out); err != nil {
		return err
	}
	if want, ok := fnpackSHA256[version+" "+runtime.GOOS+"/"+runtime.GOARCH]; ok {
		got, err := fileSHA256(out)
		if err != nil {
			return err
		}
		if !strings.EqualFold(got, want) {
			return fmt.Errorf("摘要对不上：\n got  %s\n want %s", got, want)
		}
		fmt.Printf("    摘要校验通过 %s\n", got)
	}
	return nil
}

// makeExecutable 给文件加上可执行位。
//
// 下载来的文件默认是 0644，只有 Windows 上不讲究这个；
// Linux / macOS 上没有可执行位就 fork/exec 不动。
func makeExecutable(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	return os.Chmod(path, 0o755)
}

// validateFnpack 拿一个最小包试打一下，确认这个 fnpack 在本机真能出包。
//
// 这不是多余的检查：fnpack 打包失败时也返回 0，只有真的产出 .fpk 才算成功。
//
// 只看退出码看不出来 —— fnpack 打包失败时也返回 0，
// 只能看有没有真的产出 .fpk。
func validateFnpack(bin string) error {
	dir, err := os.MkdirTemp("", "fnpack-probe-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	if err := writeProbePackage(dir); err != nil {
		return err
	}
	// 必须是绝对路径：子进程的工作目录是临时目录，相对路径在那儿解析不到。
	abs, err := filepath.Abs(bin)
	if err != nil {
		return err
	}
	// 两种打包方式都试：
	//   1.2.x 不认 --nosign，直接打就行（也没要证书这回事）；
	//   1.3.x 默认要开发者证书，加 --nosign 才放行。
	first := probePack(abs, dir, false)
	if first == nil {
		return nil
	}
	second := probePack(abs, dir, true)
	if second == nil {
		return nil
	}
	// 两种方式的错误都报出来：
	// 1.2.x 会说「不认 --nosign」，1.3.x 会说「要证书」或被杀软拦下。
	return fmt.Errorf("默认方式: %v；加 --nosign: %v", first, second)
}

// probePack 打一次探针包，看有没有真的产出 .fpk。
func probePack(bin, dir string, nosign bool) error {
	args := []string{"build", "--directory", "."}
	if nosign {
		args = append(args, "--nosign")
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	prods, _ := filepath.Glob(filepath.Join(dir, "*.fpk"))
	if len(prods) == 0 {
		return fmt.Errorf("没有产出 .fpk：%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// writeProbePackage 写一个最小可打包的应用目录。
func writeProbePackage(dir string) error {
	for _, d := range []string{"cmd", "config", "wizard", "app/bin", "app/ui/images", "app/www"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			return err
		}
	}
	manifest := strings.Join([]string{
		"appname = FnpackProbe",
		"version = 1.0.0",
		"display_name = probe",
		"desc = probe",
		"source = thirdparty",
		"platform = x86",
		"maintainer = probe",
		"maintainer_url = https://example.com",
		"distributor = probe",
		"distributor_url = https://example.com",
		"os_min_version = 0.9.0",
		"desktop_uidir = ui",
		"desktop_applaunchname = FnpackProbe.Main",
		"service_port = 9999",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "manifest"), []byte(manifest), 0o644); err != nil {
		return err
	}
	// fnpack 会检查这些生命周期脚本是不是都在（内容不检查），少一个就拒绝打包。
	for _, name := range []string{
		"main", "install_init", "install_callback",
		"uninstall_init", "uninstall_callback",
		"upgrade_init", "upgrade_callback",
		"config_init", "config_callback",
	} {
		if err := os.WriteFile(filepath.Join(dir, "cmd", name), []byte("#!/bin/bash\nexit 0\n"), 0o755); err != nil {
			return err
		}
	}
	// 向导文件也要齐（内容可以是空数组）。
	for _, name := range []string{"install", "uninstall", "upgrade", "config"} {
		if err := os.WriteFile(filepath.Join(dir, "wizard", name), []byte("[]"), 0o644); err != nil {
			return err
		}
	}
	for _, f := range []string{"privilege", "resource"} {
		if err := os.WriteFile(filepath.Join(dir, "config", f), []byte("{}"), 0o644); err != nil {
			return err
		}
	}
	// 入口配置要用 {".url": {...}} 的结构，键名要和 manifest 的
	// desktop_applaunchname 对上。用 map 生成，省得手写 JSON 转义出错。
	ui, err := json.Marshal(map[string]any{
		".url": map[string]any{
			"FnpackProbe.Main": map[string]any{
				"title":    "probe",
				"icon":     "images/icon_{0}.png",
				"type":     "iframe",
				"protocol": "http",
				"port":     "9999",
				"url":      "/",
			},
		},
	})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "app", "ui", "config"), ui, 0o644); err != nil {
		return err
	}
	// 图标必须是真 PNG：直接用画图标的那个函数生成。
	// 根目录的 ICON.PNG / ICON_256.PNG 是 fnpack 的硬性要求，1.3.x 会检查。
	if err := writeIcon(filepath.Join(dir, "app", "ui", "images", "icon_64.png"), 64); err != nil {
		return err
	}
	if err := writeIcon(filepath.Join(dir, "app", "ui", "images", "icon_256.png"), 256); err != nil {
		return err
	}
	if err := writeIcon(filepath.Join(dir, "ICON.PNG"), 64); err != nil {
		return err
	}
	return writeIcon(filepath.Join(dir, "ICON_256.PNG"), 256)
}

// readFnpackState 读本地状态，没有就返回空的。
func readFnpackState(dir string) fnpackState {
	var s fnpackState
	b, err := os.ReadFile(filepath.Join(dir, fnpackStateFile))
	if err != nil {
		return s
	}
	_ = json.Unmarshal(b, &s)
	return s
}

// writeFnpackState 写本地状态。
func writeFnpackState(dir string, s fnpackState) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, fnpackStateFile), b, 0o644)
}

// fileExists 判断路径在不在。
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// platformKeys 列出支持的平台键，给报错信息用。
func platformKeys() []string {
	keys := make([]string, 0, len(fnpackPlatforms))
	for k := range fnpackPlatforms {
		keys = append(keys, k)
	}
	return keys
}

// fileSHA256 算文件的摘要。
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
