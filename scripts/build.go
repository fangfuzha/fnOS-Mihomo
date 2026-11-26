package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// frontendFiles 是前端页面的文件清单。
//
// web/ 是前端唯一源，fnos/app/www 只是打包用的镜像 —— 两边必须一致，
// 否则装出来的是旧版页面。check_frontend 会校验这一点。
var frontendFiles = []string{"index.html", "app.css", "app.js"}

// cmdBuild 打包 fnOS 应用包（.fpk）。
//
// 用法：build [amd64|arm64|all]
//
// 打包流程只有这一份实现，没有平台专用的包装脚本：
// 同一套步骤写两遍（比如一份 PowerShell 一份 Bash），改一处漏一处是迟早的事。
func cmdBuild(args []string) error {
	root := repoRoot()
	arches, err := parseArchArgs(args)
	if err != nil {
		return err
	}
	for _, arch := range arches {
		if err := buildOne(root, arch); err != nil {
			return err
		}
	}
	return nil
}

// parseArchArgs 解析架构参数。
func parseArchArgs(args []string) ([]string, error) {
	target := "amd64"
	if len(args) > 0 {
		target = args[0]
	}
	switch target {
	case "amd64", "x86", "x86_64":
		return []string{"amd64"}, nil
	case "arm64", "arm", "aarch64":
		return []string{"arm64"}, nil
	case "all":
		return []string{"amd64", "arm64"}, nil
	default:
		return nil, fmt.Errorf("不支持的架构：%s（可用 amd64 / arm64 / all）", target)
	}
}

// buildOne 打包一个架构。
//
// 每个架构在独立的暂存目录里构建：共用一个目录会让后一次构建覆盖前一次的
// 二进制，打出来的包里装着另一个架构的可执行文件 —— 在 NAS 上表现为
// "ELF: not found" 或 "Exec format error"。
func buildOne(root string, arch string) error {
	platform := "x86"
	if arch == "arm64" {
		platform = "arm"
	}
	// 产物名带版本号：FnDepot 规范要求「版本号 + 架构」的包文件视为不可变，
	// 同名文件内容一换，客户端缓存会让一部分人永远装到旧包。
	appName, version, err := manifestAppVersion(root)
	if err != nil {
		return err
	}
	outName := fmt.Sprintf("%s-%s-x86.fpk", appName, version)
	if arch == "arm64" {
		outName = fmt.Sprintf("%s-%s-arm64.fpk", appName, version)
	}

	stage := filepath.Join(root, "build", arch)
	fmt.Printf("==> 构建 %s (platform=%s)\n", arch, platform)

	// 清空暂存目录，复制一份干净的包结构。
	if err := os.RemoveAll(stage); err != nil {
		return err
	}
	for _, d := range []string{"app/bin", "app/ui/images", "app/www"} {
		if err := os.MkdirAll(filepath.Join(stage, filepath.FromSlash(d)), 0o755); err != nil {
			return err
		}
	}

	fmt.Println("    复制包结构")
	if err := copyPackageSkeleton(root, stage); err != nil {
		return err
	}

	fmt.Println("    编译面板")
	panel := filepath.Join(stage, "app", "bin", "panel")
	if err := compilePanel(root, arch, panel); err != nil {
		return err
	}

	fmt.Println("    解压 mihomo 内核")
	gz := filepath.Join(root, "assets", "mihomo-linux-"+arch+".gz")
	if !fileExists(gz) {
		return fmt.Errorf("缺少 %s，请先运行 go run ./scripts fetch_mihomo", gz)
	}
	if _, err := ungzFile(gz, filepath.Join(stage, "app", "bin", "mihomo")); err != nil {
		return fmt.Errorf("内核解压失败: %w", err)
	}

	fmt.Println("    同步 geodata")
	if err := copyGeoData(filepath.Join(root, "assets", "geodata"), filepath.Join(stage, "app", "bin")); err != nil {
		return err
	}

	// 第三方面板归档随包分发：NAS 上装好就有本地那份，
	// 不用每次都去 GitHub 拉（国内直连 GitHub 常常失败）。
	fmt.Println("    同步第三方面板")
	if err := copyDashboardArchives(filepath.Join(root, "assets"), filepath.Join(stage, "app", "bin", "dashboards")); err != nil {
		return err
	}

	// web/ 是前端唯一源，fnos/app/www 只是打包用的镜像。
	// 这里先同步再校验，镜像就不会再出现「装出来还是旧版前端」的情况。
	fmt.Println("    同步前端镜像")
	if err := syncFrontendMirror(root); err != nil {
		return err
	}

	fmt.Println("    检查前端资源")
	if err := cmdCheckFrontend([]string{root}); err != nil {
		return fmt.Errorf("前端资源检查未通过: %w", err)
	}

	fmt.Println("    拷贝前端资源")
	for _, name := range frontendFiles {
		if err := copyFile(filepath.Join(root, "web", name), filepath.Join(stage, "app", "www", name)); err != nil {
			return err
		}
	}

	fmt.Println("    同步 manifest 平台声明")
	if err := setManifestPlatform(filepath.Join(stage, "manifest"), platform); err != nil {
		return err
	}

	fmt.Println("    规整行尾（LF）")
	if err := normalizeTextFiles(stage); err != nil {
		return err
	}

	fmt.Println("    校验二进制架构")
	if err := verifyELFArch(filepath.Join(stage, "app", "bin", "mihomo"), arch); err != nil {
		return err
	}

	// 打包器按需下载，不进版本库。
	fmt.Println("    准备 fnpack 打包器")
	fnpackDir := filepath.Join(root, "build", "fnpack")
	if err := ensureFnpack(fnpackDir); err != nil {
		return fmt.Errorf("fnpack 准备失败: %w", err)
	}
	fnpack, err := resolveFnpack(fnpackDir)
	if err != nil {
		return err
	}

	fmt.Println("    打包")
	out := filepath.Join(root, "build", outName)
	if err := packWithFnpack(fnpack, stage, out); err != nil {
		return err
	}
	info, err := os.Stat(out)
	if err != nil {
		return err
	}
	fmt.Printf("    产出 %s (%.1f MB)\n", out, float64(info.Size())/(1024*1024))
	return nil
}

// manifestAppVersion 取 manifest 里的应用名与版本号。
//
// 产物名要用它们：包名里带版本，FnDepot 才认得出每个版本对应哪个文件。
func manifestAppVersion(root string) (string, string, error) {
	mf, err := parseManifest(filepath.Join(root, "fnos", "manifest"))
	if err != nil {
		return "", "", err
	}
	appName, version := mf["appname"], mf["version"]
	if appName == "" || version == "" {
		return "", "", fmt.Errorf("manifest 里缺 appname 或 version")
	}
	return appName, version, nil
}

// copyPackageSkeleton 复制 fnOS 应用包的目录结构（不含二进制）。
func copyPackageSkeleton(root string, stage string) error {
	fnos := filepath.Join(root, "fnos")
	files := []string{"manifest", "ICON.PNG", "ICON_256.PNG"}
	for _, name := range files {
		if err := copyFile(filepath.Join(fnos, name), filepath.Join(stage, name)); err != nil {
			return err
		}
	}
	for _, dir := range []string{"cmd", "config", "wizard"} {
		if err := copyTree(filepath.Join(fnos, dir), filepath.Join(stage, dir)); err != nil {
			return err
		}
	}
	if err := copyFile(filepath.Join(fnos, "app", "ui", "config"), filepath.Join(stage, "app", "ui", "config")); err != nil {
		return err
	}
	for _, name := range []string{"icon_64.png", "icon_256.png"} {
		if err := copyFile(filepath.Join(fnos, "app", "ui", "images", name), filepath.Join(stage, "app", "ui", "images", name)); err != nil {
			return err
		}
	}
	return nil
}

// copyTree 复制整个目录。
func copyTree(src string, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		from := filepath.Join(src, e.Name())
		to := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyTree(from, to); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(from, to); err != nil {
			return err
		}
	}
	return nil
}

// compilePanel 交叉编译面板。
//
// 面板跑在 NAS 上，所以固定 GOOS=linux；CGO 关掉，免得拖一堆 .so 进来。
func compilePanel(root string, arch string, out string) error {
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", out, "./cmd/panel")
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS=linux",
		"GOARCH="+arch,
		"GOFLAGS=-mod=mod",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("面板编译失败: %w", err)
	}
	return nil
}

// syncFrontendMirror 把 web/ 同步到 fnos/app/www/。
func syncFrontendMirror(root string) error {
	for _, name := range frontendFiles {
		if err := copyFile(filepath.Join(root, "web", name), filepath.Join(root, "fnos", "app", "www", name)); err != nil {
			return err
		}
	}
	return nil
}

// platformRe 匹配 manifest 里的 platform 字段。
var platformRe = regexp.MustCompile(`(?m)^platform\s*=.*$`)

// setManifestPlatform 改写 manifest 里的 platform 字段。
//
// 改的是暂存副本，源码目录不动。
func setManifestPlatform(path string, platform string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out := platformRe.ReplaceAll(b, []byte("platform              = "+platform))
	return os.WriteFile(path, out, 0o644)
}

// normalizeTextFiles 把文本文件的行尾统一成 LF。
//
// 只处理脚本与配置这类文本：把编译产物一起喂进去，
// ELF 里的 0D 0A 会被当成行尾删掉，二进制当场损坏。
func normalizeTextFiles(stage string) error {
	var files []string
	for _, dir := range []string{"cmd", "config", "wizard"} {
		entries, err := os.ReadDir(filepath.Join(stage, dir))
		if err != nil {
			return err
		}
		for _, e := range entries {
			if !e.IsDir() {
				files = append(files, filepath.Join(stage, dir, e.Name()))
			}
		}
	}
	files = append(files,
		filepath.Join(stage, "manifest"),
		filepath.Join(stage, "app", "ui", "config"),
	)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		if !strings.Contains(string(b), "\r\n") {
			continue
		}
		if err := os.WriteFile(f, []byte(strings.ReplaceAll(string(b), "\r\n", "\n")), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// verifyELFArch 校验二进制确实是目标架构。
//
// 暂存目录串了就会装错架构，装到 NAS 上是 "ELF: not found"。
// 这里读 ELF 头的 e_machine 字段比对，比看文件大小靠谱。
func verifyELFArch(path string, arch string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	var hdr [20]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return fmt.Errorf("%s 读不出 ELF 头: %w", path, err)
	}
	if hdr[0] != 0x7f || hdr[1] != 'E' || hdr[2] != 'L' || hdr[3] != 'F' {
		return fmt.Errorf("%s 不是 ELF 文件", path)
	}
	machine := binary.LittleEndian.Uint16(hdr[18:20])
	expect := uint16(0x3E) // x86-64
	if arch == "arm64" {
		expect = 0xB7 // aarch64
	}
	if machine != expect {
		return fmt.Errorf("内核架构不匹配：期望 0x%X 实际 0x%X", expect, machine)
	}
	return nil
}

// resolveFnpack 找到打包器的可执行文件。
func resolveFnpack(dir string) (string, error) {
	name := "fnpack"
	if runtime.GOOS == "windows" {
		name = "fnpack.exe"
	}
	p := filepath.Join(dir, name)
	if fileExists(p) {
		return p, nil
	}
	// 退一步：看看 PATH 里有没有装好的 fnpack。
	if exe, err := exec.LookPath("fnpack"); err == nil {
		return exe, nil
	}
	return "", fmt.Errorf("找不到 fnpack 可执行文件（应在 %s）", p)
}

// packWithFnpack 调用打包器出包。
//
// 签名：fnpack 1.3 起打包默认要开发者证书，没配就用 --nosign；
// 但 1.2.x 不认这个参数。所以先按默认方式打一次，
// 没出包再带 --nosign 重来 —— fnpack 失败时也返回 0，只能看有没有出包。
func packWithFnpack(fnpack string, stage string, out string) error {
	var signArgs []string
	if key, cert := os.Getenv("FNPACK_KEY"), os.Getenv("FNPACK_CERT"); key != "" && cert != "" {
		fmt.Println("    用开发者证书签名打包")
		signArgs = []string{"--key", key, "--cert", cert}
	}

	run := func(nosign bool) error {
		args := []string{"build"}
		args = append(args, signArgs...)
		if nosign {
			args = append(args, "--nosign")
		}
		args = append(args, "--directory", ".")
		cmd := exec.Command(fnpack, args...)
		cmd.Dir = stage
		out, err := cmd.CombinedOutput()
		fmt.Print(string(out))
		return err
	}

	if err := run(false); err != nil {
		return fmt.Errorf("fnpack 打包失败: %w", err)
	}
	produced, err := findFPK(stage)
	if err != nil {
		// 没出包就带 --nosign 再来一次。
		fmt.Println("    没出包，加 --nosign 再打一次")
		if err := run(true); err != nil {
			return fmt.Errorf("fnpack 打包失败: %w", err)
		}
		produced, err = findFPK(stage)
		if err != nil {
			return err
		}
	}

	if err := copyFile(produced, out); err != nil {
		return err
	}
	return nil
}

// findFPK 在目录里找打出来的安装包。
func findFPK(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".fpk") {
			return filepath.Join(dir, e.Name()), nil
		}
	}
	return "", fmt.Errorf("打包没有产出 .fpk")
}
