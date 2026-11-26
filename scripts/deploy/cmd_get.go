package deploy

import (
	"flag"
	"fmt"
)

// cmdGet 从 NAS 下载文件到本地。
//
// 用法: go run ./scripts deploy get -src /tmp/Mihomo.fpk ./Mihomo.fpk
//
// 与 put 互为反向操作：fnpack 1.3.x 在 Windows 上打不出包，
// 需要把暂存目录传过去、在 Linux 上打包，再把 .fpk 拉回来。
func cmdGet(args []string) error {
	fs := flag.NewFlagSet("get", flag.ExitOnError)
	common := addCommon(fs)
	src := fs.String("src", "", "远端路径")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) < 1 {
		return fmt.Errorf("用法: go run ./scripts deploy get -src <远端路径> <本地路径>")
	}
	local := rest[0]
	if *src == "" {
		return fmt.Errorf("必须给 -src <远端路径>")
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

	if err := c.Get(*src, local); err != nil {
		return err
	}
	fmt.Println("已下载 " + *src + " -> " + local)
	return nil
}
