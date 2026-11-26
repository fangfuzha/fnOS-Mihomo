// scripts 是 fnOS-Mihomo 的维护脚本集。
//
// 全部用 Go 实现：打包机上不必先装 Python，
// Windows / Linux / macOS 都能直接跑。
//
// 打包相关的子命令只依赖标准库；
// deploy 子命令（NAS 部署）额外依赖 golang.org/x/crypto/ssh 与 github.com/pkg/sftp。
//
// 用法：fnos-mihomo-tools <子命令> [参数...]
//
//	check_frontend            校验前端资源完整性
//	copyfiles <源> <目标>      拷贝随包分发的 geodata
//	copy_dashboard <源> <目标>  拷贝随包分发的第三方面板归档
//	ungz <源.gz> <目标>        解压 gzip
//	mkicon <64.png> <256.png>  生成应用图标
//	fetch_mihomo              下载 mihomo 内核到 assets/
//	fetch_dashboard           下载第三方面板归档到 assets/
//	fetch_geodata             下载 geodata 到 assets/geodata/
//	fetch_fnpack               下载 fnpack 打包器到 build/fnpack/
//	build [amd64|arm64|all]    打包 fnOS 应用包（.fpk）
//	fndepot build|publish|check   FnDepot 外部源索引（fnpack.json）
//	deploy <子命令>           NAS 部署：exec / put / get / hot-update / fpk / verify / hostkey
package main

import (
	"fmt"
	"os"

	"fnos-mihomo/scripts/deploy"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "check_frontend":
		err = cmdCheckFrontend(os.Args[2:])
	case "copyfiles":
		err = cmdCopyFiles(os.Args[2:])
	case "ungz":
		err = cmdUngz(os.Args[2:])
	case "mkicon":
		err = cmdMkicon(os.Args[2:])
	case "fetch_mihomo":
		err = cmdFetchMihomo(os.Args[2:])
	case "fetch_dashboard":
		err = cmdFetchDashboard(os.Args[2:])
	case "fetch_geodata":
		err = cmdFetchGeodata(os.Args[2:])
	case "fetch_fnpack":
		err = cmdFetchFnpack(os.Args[2:])
	case "copy_dashboard":
		err = cmdCopyDashboard(os.Args[2:])
	case "build":
		err = cmdBuild(os.Args[2:])
	case "fndepot":
		err = cmdFnDepot(os.Args[2:])
	case "deploy":
		err = deploy.Run(os.Args[2:])
	case "help", "-h", "--help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %s\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr,
		"fnOS-Mihomo 维护脚本\n\n"+
			"用法:\n"+
			"  fnos-mihomo-tools check_frontend             校验前端资源完整性\n"+
			"  fnos-mihomo-tools copyfiles <源目录> <目标目录>  拷贝 geodata 文件\n"+
			"  fnos-mihomo-tools ungz <源.gz> <目标文件>       解压 gzip 文件\n"+
			"  fnos-mihomo-tools mkicon <64.png> <256.png>     生成应用图标\n"+
			"  fnos-mihomo-tools fetch_mihomo                下载 mihomo 内核到 assets/\n"+
			"  fnos-mihomo-tools fetch_dashboard             下载第三方面板归档到 assets/\n"+
			"  fnos-mihomo-tools fetch_geodata               下载 geodata 到 assets/geodata/\n"+
			"  fnos-mihomo-tools fetch_fnpack                 下载 fnpack 打包器到 build/fnpack/\n"+
			"  fnos-mihomo-tools copy_dashboard <源> <目标>   拷贝第三方面板归档\n"+
			"  fnos-mihomo-tools build [amd64|arm64|all]      打包 fnOS 应用包（.fpk）\n"+
			"  fnos-mihomo-tools fndepot build|publish|check  FnDepot 外部源索引（fnpack.json）\n"+
			"  fnos-mihomo-tools deploy <子命令>             NAS 部署：exec/put/get/hot-update/fpk/verify/hostkey\n")
}
