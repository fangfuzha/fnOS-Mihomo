// Package deploy 是 NAS 部署工具。
//
// 通过 SSH/SFTP 连到飞牛 NAS，做部署与排查：换面板二进制与前端、
// 传安装包、在 NAS 上执行命令、看运行状态。
//
// 参数从项目根目录的 .env 读 —— NAS 地址与密码只放那儿，不进版本库。
//
// 用法：go run ./scripts deploy <子命令> [参数]
package deploy

import (
	"flag"
	"fmt"
	"os"
)

// commonFlags 是各子命令共用的参数。
type commonFlags struct {
	envFile  string
	insecure bool
}

// addCommon 把通用参数挂到子命令的 flag set 上。
func addCommon(fs *flag.FlagSet) *commonFlags {
	c := &commonFlags{}
	fs.StringVar(&c.envFile, "env", defaultEnvPath(), "参数文件路径（默认项目根的 .env）")
	fs.BoolVar(&c.insecure, "insecure", false, "跳过 host key 校验，仅首次取指纹时用")
	return c
}

// Run 是入口。
//
// scripts 主程序把 "deploy" 后面的参数原样交过来，
// 于是所有维护命令都从同一个入口走：go run ./scripts <子命令>。
func Run(args []string) error {
	if len(args) < 1 {
		usage()
		return fmt.Errorf("缺少子命令")
	}
	var err error
	switch args[0] {
	case "exec":
		err = cmdExec(args[1:])
	case "put":
		err = cmdPut(args[1:])
	case "get":
		err = cmdGet(args[1:])
	case "hot-update":
		err = cmdHotUpdate(args[1:])
	case "fpk":
		err = cmdFpk(args[1:])
	case "verify":
		err = cmdVerify(args[1:])
	case "hostkey":
		err = cmdHostKey(args[1:])
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %s\n\n", args[0])
		usage()
		return fmt.Errorf("未知子命令: %s", args[0])
	}
	return err
}

func usage() {
	fmt.Fprint(os.Stderr,
		"NAS 部署工具\n\n"+
			"用法:\n"+
			"  go run ./scripts deploy exec -c \"cmd\"        在 NAS 上执行命令（-c 可重复，或 -f cmds.json）\n"+
			"  go run ./scripts deploy put <本地路径>          上传文件到 NAS\n"+
			"  go run ./scripts deploy get -src <远端> <本地>   从 NAS 下载文件\n"+
			"  go run ./scripts deploy hot-update              热更新面板二进制与前端（内核与订阅不动）\n"+
			"  go run ./scripts deploy fpk                     上传 .fpk 到 NAS 并检查包内容\n"+
			"  go run ./scripts deploy verify                  只读体检：运行状态、控制口、面板目录、日志、进程\n"+
			"  go run ./scripts deploy hostkey                 打印 NAS 的 host key（写进 known_hosts 用）\n\n"+
			"通用参数:\n"+
			"  -env <文件>    参数文件路径，默认项目根目录的 .env\n"+
			"  -insecure      跳过 host key 校验，仅首次取指纹时用\n")
}
