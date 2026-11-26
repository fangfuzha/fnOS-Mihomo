//go:build !windows

package safeio

// isRenameContention 在非 Windows 上一律返回 false：
// Linux 的 rename 覆盖是原子的，不会有争用。
func isRenameContention(error) bool { return false }
