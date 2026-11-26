package core

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrAlreadyRunning 表示内核已经在运行。
//
// 调用方的态度不一样：自动启停把它当成「目标已达成」，
// 手动接口把它当成「你要启动的东西已经在跑了」，都不该算失败。
var ErrAlreadyRunning = errors.New("内核已在运行")

// Core 负责 mihomo 内核进程的启停与日志采集。
type Core struct {
	mu sync.Mutex

	// lifeMu 把「停 + 起」这组动作串成一步。
	//
	// Restart 是 Stop 加 Start，两步各自加锁时两个 goroutine 会交错：
	// 一个停了内核，另一个拉起来，第一个再 start 就报「内核已在运行」；
	// 更糟的是第一个的 stop 会把第二个刚拉起来的内核又停掉。
	lifeMu sync.Mutex

	binPath    string
	configPath string
	workDir    string
	logPath    string

	cmd     *exec.Cmd
	exited  chan struct{}
	started time.Time

	logMu  sync.Mutex
	logBuf []string
}

func New(binPath, configPath, workDir string) *Core {
	return &Core{
		binPath:    binPath,
		configPath: configPath,
		workDir:    workDir,
		logPath:    filepath.Join(workDir, "mihomo.log"),
	}
}

// IsRunning 判断内核进程是否还活着。
func (c *Core) IsRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd == nil || c.cmd.Process == nil {
		return false
	}
	return c.cmd.ProcessState == nil
}

// Start 启动内核。已运行时直接返回错误，避免出现两个实例抢端口。
func (c *Core) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cmd != nil && c.cmd.Process != nil && c.cmd.ProcessState == nil {
		return ErrAlreadyRunning
	}
	if _, err := os.Stat(c.binPath); err != nil {
		return fmt.Errorf("找不到内核文件 %s: %w", c.binPath, err)
	}
	if _, err := os.Stat(c.configPath); err != nil {
		return fmt.Errorf("找不到配置文件 %s: %w", c.configPath, err)
	}

	if err := os.MkdirAll(c.workDir, 0o755); err != nil {
		return err
	}

	logFile, err := os.OpenFile(c.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	// 父进程用完就关：子进程已经拿到了自己那份句柄。
	// 留着不关就是每次重启泄一个 fd，攒多了会把文件也锁住。
	ownFile := err == nil
	if !ownFile {
		logFile = os.Stdout
	}
	defer func() {
		if ownFile {
			_ = logFile.Close()
		}
	}()

	cmd := exec.Command(c.binPath, "-d", c.workDir, "-f", c.configPath)
	cmd.Dir = c.workDir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(), "HOME="+c.workDir)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动内核失败: %w", err)
	}
	c.cmd = cmd
	c.started = time.Now()

	// exited 只由这一个协程关闭：内核真的退出了才关。
	// Stop 靠它确认「进程已经没了」，而不是靠再调一次 Wait。
	exited := make(chan struct{})
	c.exited = exited

	go func() {
		_ = cmd.Wait()
		close(exited)
		c.appendLog("[core] 内核进程已退出")
	}()

	c.appendLog("[core] 内核已启动 pid=" + fmt.Sprint(cmd.Process.Pid))
	return nil
}

// Stop 停止内核，给足时间优雅退出。
func (c *Core) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cmd == nil || c.cmd.Process == nil {
		return nil
	}

	exited := c.exited
	_ = c.cmd.Process.Signal(os.Interrupt)
	// 等进程真的退出。
	//
	// 这里不能再调一次 cmd.Wait()：第一次 Wait 已经被 Start 的协程占着，
	// 第二次会立刻返回「Wait was already called」，
	// 于是 Stop 不等进程就返回，新内核起来时老内核还占着端口。
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		_ = c.cmd.Process.Kill()
		<-exited
	}

	c.cmd = nil
	c.appendLog("[core] 内核已停止")
	return nil
}

// Restart 重启内核，用于配置变更后生效。
func (c *Core) Restart() error {
	// 整个「停 + 起」必须是一步，不能各走各的锁。
	c.lifeMu.Lock()
	defer c.lifeMu.Unlock()

	if err := c.Stop(); err != nil {
		return err
	}
	time.Sleep(500 * time.Millisecond)
	return c.Start()
}

// Uptime 返回内核已运行时长。
func (c *Core) Uptime() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd == nil || c.started.IsZero() {
		return 0
	}
	return time.Since(c.started)
}

func (c *Core) appendLog(line string) {
	c.logMu.Lock()
	defer c.logMu.Unlock()
	c.logBuf = append(c.logBuf, line)
	if len(c.logBuf) > 500 {
		c.logBuf = c.logBuf[len(c.logBuf)-500:]
	}
}

// TailLog 读取日志文件末尾若干行。
func (c *Core) TailLog(n int) []string {
	b, err := os.ReadFile(c.logPath)
	if err != nil {
		c.logMu.Lock()
		defer c.logMu.Unlock()
		return append([]string(nil), c.logBuf...)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// Version 读取内核版本号。
func (c *Core) Version() string {
	out, err := exec.Command(c.binPath, "-v").CombinedOutput()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}
