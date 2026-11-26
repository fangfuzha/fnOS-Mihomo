package deploy

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// testServer 是进程内的 SSH 服务端。
//
// 真 NAS 从这台机器连不上（IPv6 前缀不同），所以自己起一个：
// SSH 客户端、SFTP、sudo 密码走 stdin 这几条路径都能真跑一遍。
type testServer struct {
	addr    string
	hostKey ssh.PublicKey
	dir     string
}

func startTestSSH(t *testing.T) *testServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == "tester" && string(pass) == "s3cret" {
				return nil, nil
			}
			return nil, fmt.Errorf("认证失败")
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	dir := t.TempDir()
	s := &testServer{addr: ln.Addr().String(), hostKey: signer.PublicKey(), dir: dir}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn, cfg)
		}
	}()
	return s
}

// serve 处理一条连接：exec 请求回显命令与 stdin，sftp 子系统交给 sftp 服务端。
func (s *testServer) serve(conn net.Conn, cfg *ssh.ServerConfig) {
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer sshConn.Close()
	go ssh.DiscardRequests(reqs)

	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			newCh.Reject(ssh.UnknownChannelType, "只支持 session")
			continue
		}
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			continue
		}
		go s.handleSession(ch, chReqs)
	}
}

func (s *testServer) handleSession(ch ssh.Channel, chReqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range chReqs {
		switch req.Type {
		case "exec":
			// exec 的载荷是 4 字节长度 + 命令本身
			if len(req.Payload) < 4 {
				req.Reply(false, nil)
				return
			}
			cmdLen := binary.BigEndian.Uint32(req.Payload[:4])
			if int(cmdLen)+4 > len(req.Payload) {
				req.Reply(false, nil)
				return
			}
			cmd := string(req.Payload[4 : 4+cmdLen])
			fmt.Fprintf(ch, "OK:%s", cmd)
			req.Reply(true, nil)
			// stdin 读完才结束：客户端写完密码会关闭它
			in, _ := io.ReadAll(ch)
			if len(in) > 0 {
				fmt.Fprintf(ch, "\nSTDIN:%s", strings.TrimRight(string(in), "\n"))
			}
			// 退出状态必须显式回：否则客户端 wait 会报
			// "remote command exited without exit status"。
			status := struct{ Status uint32 }{0}
			ch.SendRequest("exit-status", false, ssh.Marshal(status))
			// 随后关掉通道：客户端在等 stdout 的 EOF，不关就一起挂死。
			return
		case "subsystem":
			if len(req.Payload) < 4 || string(req.Payload[4:]) != "sftp" {
				req.Reply(false, nil)
				continue
			}
			req.Reply(true, nil)
			server, err := sftp.NewServer(ch)
			if err != nil {
				return
			}
			server.Serve()
			server.Close()
			return
		default:
			req.Reply(false, nil)
		}
	}
}

// writeKnownHosts 写一份 known_hosts 文件。
func writeKnownHosts(t *testing.T, path string, addr string, key ssh.PublicKey) {
	t.Helper()
	// known_hosts 里非 22 端口的写法是 [host]:port
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf("[%s]:%s %s %s\n", host, port, key.Type(), base64.StdEncoding.EncodeToString(key.Marshal()))
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

// testEnv 组一个指向测试服务端的配置。
func testEnv(t *testing.T, s *testServer, knownHosts string) *Env {
	t.Helper()
	host, port, err := net.SplitHostPort(s.addr)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := strconv.Atoi(port)
	return &Env{
		Host:       host,
		Port:       p,
		User:       "tester",
		Pass:       "s3cret",
		SudoPass:   "s3cret",
		KnownHosts: knownHosts,
		RemoteTmp:  "/tmp",
	}
}

// TestDialAndRun 连上测试服务端并执行命令。
func TestDialAndRun(t *testing.T) {
	s := startTestSSH(t)
	kh := filepath.Join(t.TempDir(), "known_hosts")
	writeKnownHosts(t, kh, s.addr, s.hostKey)

	c, err := dial(testEnv(t, s, kh), false)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer c.Close()

	out, err := c.Run("echo hi", false, 30*time.Second)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "OK:echo hi") {
		t.Errorf("输出不对: %q", out)
	}
}

// TestSudoPasswordGoesToStdin sudo 的密码必须走 stdin，不能出现在命令行里。
func TestSudoPasswordGoesToStdin(t *testing.T) {
	s := startTestSSH(t)
	kh := filepath.Join(t.TempDir(), "known_hosts")
	writeKnownHosts(t, kh, s.addr, s.hostKey)

	c, err := dial(testEnv(t, s, kh), false)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer c.Close()

	out, err := c.Run("id", true, 30*time.Second)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "STDIN:s3cret") {
		t.Errorf("密码没有通过 stdin 送出: %q", out)
	}
	if !strings.Contains(out, "sudo -S") {
		t.Errorf("命令没有走 sudo -S: %q", out)
	}
}

// TestHostKeyMismatch known_hosts 对不上时必须拒绝连接。
func TestHostKeyMismatch(t *testing.T) {
	s := startTestSSH(t)
	other := startTestSSH(t)
	kh := filepath.Join(t.TempDir(), "known_hosts")
	writeKnownHosts(t, kh, other.addr, other.hostKey)

	if _, err := dial(testEnv(t, s, kh), false); err == nil {
		t.Fatal("主机密钥不匹配时不该连上")
	}
}

// TestPut 上传文件。
func TestPut(t *testing.T) {
	s := startTestSSH(t)
	kh := filepath.Join(t.TempDir(), "known_hosts")
	writeKnownHosts(t, kh, s.addr, s.hostKey)

	c, err := dial(testEnv(t, s, kh), false)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer c.Close()

	local := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(local, []byte("内容"), 0o644); err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(s.dir, "a.txt")
	if err := c.Put(local, remote); err != nil {
		t.Fatalf("上传失败: %v", err)
	}
	b, err := os.ReadFile(remote)
	if err != nil {
		t.Fatalf("远端没有文件: %v", err)
	}
	if string(b) != "内容" {
		t.Errorf("内容不对: %q", b)
	}
}
