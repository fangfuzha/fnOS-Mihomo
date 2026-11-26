package deploy

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path"
	"strings"
	"time"
)

// multiFlag 收集可以重复给的参数。
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, "\n") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// command 是一条待执行的命令。
//
// 不用 json 标签：encoding/json 本来就按字段名（忽略大小写）匹配，
// 所以 {"cmd":"...","sudo":true} 能直接对上 Cmd 与 Sudo。
type command struct {
	Cmd  string
	Sudo bool
}

// cmdExec 在 NAS 上执行命令。
//
// 用法：
//
//	go run ./scripts deploy exec -c "uname -a" -c "ls /tmp"
//	go run ./scripts deploy exec -f cmds.json
//
// 命令清单 JSON 的格式：[{"cmd":"...","sudo":true}]
func cmdExec(args []string) error {
	fs := flag.NewFlagSet("exec", flag.ExitOnError)
	common := addCommon(fs)
	var cmds multiFlag
	fs.Var(&cmds, "c", "要执行的命令（可重复）")
	listFile := fs.String("f", "", "命令清单 JSON 文件")
	useSudo := fs.Bool("sudo", false, "以 root 执行")
	if err := fs.Parse(args); err != nil {
		return err
	}

	items, err := loadCommands(*listFile, cmds)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return fmt.Errorf("没有要执行的命令：给 -c 或 -f")
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

	for _, it := range items {
		fmt.Println("$ " + it.Cmd)
		out, err := c.Run(it.Cmd, it.Sudo || *useSudo, 120*time.Second)
		fmt.Print(out)
		if err != nil {
			return err
		}
	}
	return nil
}

// loadCommands 组装命令列表：清单文件里的在前，-c 给的在后。
func loadCommands(listFile string, inline multiFlag) ([]command, error) {
	var items []command
	if listFile != "" {
		b, err := os.ReadFile(listFile)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &items); err != nil {
			return nil, fmt.Errorf("解析命令清单 %s 失败: %w", listFile, err)
		}
	}
	for _, c := range inline {
		items = append(items, command{Cmd: c})
	}
	return items, nil
}

// cmdPut 上传一个本地文件到 NAS。
func cmdPut(args []string) error {
	fs := flag.NewFlagSet("put", flag.ExitOnError)
	common := addCommon(fs)
	dest := fs.String("dest", "", "远端路径")
	useSudo := fs.Bool("sudo", false, "用 root 写入（先传临时目录再挪过去）")
	mode := fs.String("mode", "644", "远端文件权限，如 755")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) < 1 {
		return fmt.Errorf("用法: go run ./scripts deploy put [-dest <远端路径>] [-sudo] [-mode 755] <本地路径>")
	}
	local := rest[0]
	remote := *dest
	if remote == "" {
		remote = remoteJoin("/tmp", path.Base(local))
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

	if *useSudo {
		return c.PutTo(local, remote, *mode)
	}
	if err := c.Put(local, remote); err != nil {
		return err
	}
	fmt.Println("已上传 " + local + " -> " + remote)
	return nil
}
