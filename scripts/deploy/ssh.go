package deploy

import (
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Conn 是到 NAS 的连接。
type Conn struct {
	cli  *ssh.Client
	sftp *sftp.Client
	env  *Env
}

// dial 连上 NAS。
//
// host key 校验默认开着：known_hosts 里没有这台机器就直接失败。
// 原来的 Python 版用 paramiko 的 AutoAddPolicy，等于任何主机密钥都接受，
// 中间人可以悄悄接管整条 SSH 通道 —— 密码就会直接落到对方手里。
func dial(env *Env, insecure bool) (*Conn, error) {
	cb, err := hostKeyCallback(env.KnownHosts, insecure)
	if err != nil {
		return nil, err
	}

	cfg := &ssh.ClientConfig{
		User:            env.User,
		Auth:            []ssh.AuthMethod{},
		HostKeyCallback: cb,
		Timeout:         30 * time.Second,
	}
	if env.KeyFile != "" {
		signer, err := loadKey(env.KeyFile)
		if err != nil {
			return nil, err
		}
		cfg.Auth = append(cfg.Auth, ssh.PublicKeys(signer))
	} else {
		cfg.Auth = append(cfg.Auth, ssh.Password(env.Pass))
	}

	addr := net.JoinHostPort(env.Host, fmt.Sprint(env.Port))
	cli, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("连接 %s 失败: %w", addr, err)
	}
	sf, err := sftp.NewClient(cli)
	if err != nil {
		cli.Close()
		return nil, fmt.Errorf("打开 SFTP 失败: %w", err)
	}
	return &Conn{cli: cli, sftp: sf, env: env}, nil
}

// hostKeyCallback 决定怎么校验主机密钥。
func hostKeyCallback(path string, insecure bool) (ssh.HostKeyCallback, error) {
	if insecure {
		fmt.Fprintln(os.Stderr, "警告：已跳过 host key 校验（-insecure），这条连接可能被中间人劫持")
		return ssh.InsecureIgnoreHostKey(), nil
	}
	if path == "" {
		return nil, fmt.Errorf("没有配置 NAS_KNOWN_HOSTS；首次连接请先用 hostkey 子命令取指纹")
	}
	cb, err := knownhosts.New(path)
	if err != nil {
		return nil, fmt.Errorf("读取 known_hosts (%s) 失败: %w", path, err)
	}
	return cb, nil
}

// loadKey 读 SSH 私钥。
func loadKey(path string) (ssh.Signer, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(b)
	if err != nil {
		return nil, fmt.Errorf("解析私钥 %s 失败: %w", path, err)
	}
	return signer, nil
}

// Close 断开连接。
func (c *Conn) Close() {
	if c.sftp != nil {
		c.sftp.Close()
	}
	if c.cli != nil {
		c.cli.Close()
	}
}

// Run 在 NAS 上执行一条命令并返回输出。
//
// sudo 用 sudo -S，密码写进 stdin —— 不出现在命令行里，ps 看不到。
// 原来的脚本是 "echo 密码 | sudo -S sh -c ..."，密码明文躺在进程列表中。
func (c *Conn) Run(cmd string, sudo bool, timeout time.Duration) (string, error) {
	if sudo {
		cmd = "sudo -S -p '' sh -c " + shQuote(cmd)
	}
	sess, err := c.cli.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()

	buf := &syncBuffer{}
	sess.Stdout = buf
	sess.Stderr = buf

	stdin, err := sess.StdinPipe()
	if err != nil {
		return "", err
	}
	// NAS 上这个账号的家目录不存在，登录 shell 每次都会先抱怨一句
	// "Could not chdir to home directory"，混在命令输出里很难看。
	// 用 env 请求把 HOME 指到 /，让它没有家目录可退 —— 我们的命令都用绝对路径。
	_ = sess.Setenv("HOME", "/")
	if err := sess.Start(cmd); err != nil {
		return "", err
	}
	if sudo {
		if _, err := io.WriteString(stdin, c.env.SudoPass+"\n"); err != nil {
			return buf.String(), err
		}
	}
	// 关掉 stdin：sudo 读到密码行之后要看到 EOF 才会继续。
	stdin.Close()

	// 超时要真的生效：命令卡住时不能把调用方一起挂死。
	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()
	select {
	case err = <-done:
	case <-time.After(timeout):
		sess.Close()
		return buf.String(), fmt.Errorf("命令超时（%s）", timeout)
	}
	if err != nil {
		return stripShellNoise(buf.String()), fmt.Errorf("命令执行失败: %w\n%s", err, stripShellNoise(buf.String()))
	}
	return stripShellNoise(buf.String()), nil
}

// Put 上传一个本地文件。
func (c *Conn) Put(local string, remote string) error {
	f, err := os.Open(local)
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}
	rf, err := c.sftp.Create(remote)
	if err != nil {
		return fmt.Errorf("创建远端文件 %s 失败: %w", remote, err)
	}
	defer rf.Close()

	if _, err := io.Copy(rf, f); err != nil {
		return err
	}
	// 权限跟着本地走：面板二进制必须可执行。
	return c.sftp.Chmod(remote, info.Mode().Perm())
}

// Get 下载远端文件到本地。
func (c *Conn) Get(remote string, local string) error {
	rf, err := c.sftp.Open(remote)
	if err != nil {
		return fmt.Errorf("打开远端文件 %s 失败: %w", remote, err)
	}
	defer rf.Close()

	lf, err := os.Create(local)
	if err != nil {
		return err
	}
	defer lf.Close()

	if _, err := io.Copy(lf, rf); err != nil {
		return err
	}
	return lf.Close()
}

// PutTo 上传到临时目录，再由 sudo 挪到目标位置。
//
// 应用目录下的文件普通用户写不进去，所以要「先传到 /tmp，再 sudo 挪」。
func (c *Conn) PutTo(local string, dest string, mode string) error {
	tmp := remoteJoin(c.env.RemoteTmp, path.Base(dest))
	if err := c.Put(local, tmp); err != nil {
		return err
	}
	script := fmt.Sprintf("mkdir -p %s && cp %s %s && chmod %s %s && rm -f %s",
		shQuote(path.Dir(dest)), shQuote(tmp), shQuote(dest), mode, shQuote(dest), shQuote(tmp))
	out, err := c.Run(script, true, 60*time.Second)
	if err != nil {
		return fmt.Errorf("替换 %s 失败: %w\n%s", dest, err, out)
	}
	return nil
}

// remoteJoin 拼远端路径。
//
// 远端是 Linux，路径必须用正斜杠 —— filepath.Join 在 Windows 上会拼出
// 反斜杠（\tmp\panel.new），SFTP 会直接拒绝。
func remoteJoin(elem ...string) string {
	return path.Join(elem...)
}

// stripShellNoise 去掉登录 shell 的启动噪音。
//
// NAS 上这个账号在 /etc/passwd 里的家目录不存在，shell 每次启动都会先打印
// 一句 "Could not chdir to home directory" —— 那是 shell 自己的事，
// 与命令输出无关，混在里面很难看。
//
// 改 HOME 环境变量没用：shell 是从 /etc/passwd 查家目录的。
func stripShellNoise(s string) string {
	if !strings.Contains(s, "Could not chdir to home directory") {
		return s
	}
	lines := strings.Split(s, "\n")
	kept := lines[:0]
	for _, l := range lines {
		if strings.Contains(l, "Could not chdir to home directory") {
			continue
		}
		kept = append(kept, l)
	}
	return strings.Join(kept, "\n")
}

// syncBuffer 是并发安全的输出缓冲。
//
// 超时那条路上要一边等命令结束一边读输出，strings.Builder 并发写会炸。
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// shQuote 用单引号包住一段 shell 脚本。
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
