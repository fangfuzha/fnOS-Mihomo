// Package safeio 提供「先写临时文件再改名」的原子写入。
package safeio

import (
	"os"
	"path/filepath"
)

// WriteFile 原子写入文件：先写同目录下的临时文件，再改名盖掉目标。
//
// 目标文件要么是旧内容、要么是完整的新内容，不会读到半份 ——
// 改名的替换是原子的。
//
// 临时文件名每次唯一，不能共用固定的 .tmp：
// 两个 goroutine 同时写同一个目标时，共用 .tmp 会出两种事：
//
//   - 一个先改名走，另一个再改名就报「no such file or directory」；
//   - 两边同时往同一个 .tmp 写，最终落地的是半份文件。
//
// 面板启动时后台刷新订阅与自动启停会同时写 config.yaml，
// 第一种事真发生过（NAS 日志里那条 rename 报错就是它）。
func WriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp.*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	// 改名成功后这个路径就不存在了，Remove 报错也无所谓；
	// 中途失败时靠它清掉残留。
	defer os.Remove(name)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// CreateTemp 建出来的是 0600，按调用方要求给权限。
	if err := os.Chmod(name, perm); err != nil {
		return err
	}
	// 改名走 Rename：Windows 上并发改名到同一目标会撞争用，要重试。
	return Rename(name, path)
}
