package deploy

import (
	"flag"
	"fmt"
	"os"
	"path"
	"strings"
	"time"
)

// cmdFpk 上传 .fpk 到 NAS 并检查包内容。
//
// 对应原来 upload_fpk.py 与 verify_fpk.py 两件事：
// 上传 + 解包看 manifest 与目录结构。
func cmdFpk(args []string) error {
	fs := flag.NewFlagSet("fpk", flag.ExitOnError)
	common := addCommon(fs)
	skipUpload := fs.Bool("skip-upload", false, "只检查 NAS 上已有的包")
	remote := fs.String("remote", "", "远端包路径")
	if err := fs.Parse(args); err != nil {
		return err
	}

	env, err := resolveEnv(common.envFile)
	if err != nil {
		return err
	}
	remotePath := *remote
	if remotePath == "" {
		remotePath = remoteJoin(env.RemoteTmp, path.Base(env.FpkFile))
	}

	c, err := dial(env, common.insecure)
	if err != nil {
		return err
	}
	defer c.Close()

	if !*skipUpload {
		if _, err := os.Stat(env.FpkFile); err != nil {
			return fmt.Errorf("找不到安装包 %s（先跑 go run ./scripts build 打包）", env.FpkFile)
		}
		if err := c.Put(env.FpkFile, remotePath); err != nil {
			return err
		}
		fmt.Println("已上传 " + env.FpkFile + " -> " + remotePath)
	}

	script := strings.Join([]string{
		"echo === manifest 关键字段 ===",
		fmt.Sprintf("tar -xzOf %s manifest | grep -E %s", shQuote(remotePath), shQuote("^(appname|version|platform|service_port)")),
		"echo === fpk 顶层 ===",
		fmt.Sprintf("tar -tzf %s | head -8", shQuote(remotePath)),
		"echo === app.tgz 内容 ===",
		fmt.Sprintf("tar -xzOf %s app.tgz | tar -tz | head -16", shQuote(remotePath)),
		"echo === 包内 manifest ===",
		fmt.Sprintf("tar -xzOf %s app.tgz | tar -xzO manifest | head -14", shQuote(remotePath)),
	}, "\n")

	out, err := c.Run(script, false, 90*time.Second)
	fmt.Print(out)
	return err
}
