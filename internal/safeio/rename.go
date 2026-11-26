package safeio

import (
	"os"
	"time"
)

// renameAttempts 是撞上瞬时争用时的重试次数。
const renameAttempts = 8

// Rename 改名，碰上瞬时争用就退避重试。
//
// Windows 上把多个文件并发改名到同一个目标会报「拒绝访问」：
// MoveFileEx 替换目标时，目标正被上一次的改名占着。
// 本机实测 8 路并发必现 errno=5，退避重试后零失败（19~35ms 内完成）。
//
// Linux 上 rename 覆盖是原子的，不存在这个争用 ——
// 所以平台差异只落在「这个错误算不算瞬时」这一个判断上。
//
// 导出是因为面板归档下载完也要改名到正式位置，那里的争用和写文件时一样。
func Rename(from, to string) error {
	err := os.Rename(from, to)
	if err == nil || !isRenameContention(err) {
		return err
	}
	for i := 0; i < renameAttempts; i++ {
		time.Sleep(time.Duration(5*(i+1)) * time.Millisecond)
		err = os.Rename(from, to)
		if err == nil {
			return nil
		}
		if !isRenameContention(err) {
			return err
		}
	}
	return err
}
