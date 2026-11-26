//go:build windows

package safeio

import (
	"errors"
	"syscall"
)

const (
	// errnoSharingViolation 是「文件正被别的进程使用」。
	// Go 的 syscall 包没导出这个码，按 Windows 错误码写死。
	errnoSharingViolation = syscall.Errno(32)
	// errnoLockViolation 是「文件被锁住」。
	errnoLockViolation = syscall.Errno(33)
)

// isRenameContention 判断错误是不是「目标正被占着」这种瞬时争用。
func isRenameContention(err error) bool {
	var e syscall.Errno
	if !errors.As(err, &e) {
		return false
	}
	return e == syscall.ERROR_ACCESS_DENIED ||
		e == errnoSharingViolation ||
		e == errnoLockViolation
}
