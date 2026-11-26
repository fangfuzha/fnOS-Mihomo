package deploy

import (
	"encoding/base64"
	"flag"
	"fmt"
	"net"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"
)

// cmdHostKey 打印 NAS 的 host key。
//
// 首次连接时用：把打印出来的那行追加到 known_hosts，之后就不用 -insecure 了。
// 这一步刻意不做校验 —— 目的正是先把密钥记下来，
// 但要注意：这一步得到的指纹只能通过别的渠道（控制台直连）核对一次，
// 否则第一次连接仍然可能被中间人抢先。
func cmdHostKey(args []string) error {
	fs := flag.NewFlagSet("hostkey", flag.ExitOnError)
	common := addCommon(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	env, err := resolveEnv(common.envFile)
	if err != nil {
		return err
	}

	var captured ssh.PublicKey
	cfg := &ssh.ClientConfig{
		User:    env.User,
		Auth:    []ssh.AuthMethod{ssh.Password(env.Pass)},
		Timeout: 30 * time.Second,
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			captured = key
			return nil
		},
	}
	if env.KeyFile != "" {
		signer, err := loadKey(env.KeyFile)
		if err != nil {
			return err
		}
		cfg.Auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
	}

	addr := net.JoinHostPort(env.Host, strconv.Itoa(env.Port))
	cli, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return fmt.Errorf("连接 %s 失败: %w", addr, err)
	}
	defer cli.Close()

	if captured == nil {
		return fmt.Errorf("没有拿到主机密钥")
	}
	host := env.Host
	if env.Port != 22 {
		host = "[" + host + "]:" + strconv.Itoa(env.Port)
	}
	line := host + " " + captured.Type() + " " + base64.StdEncoding.EncodeToString(captured.Marshal())
	fmt.Println(line)
	if env.KnownHosts != "" {
		fmt.Println("把这行追加到 " + env.KnownHosts + " 就能免掉 -insecure")
	}
	return nil
}
