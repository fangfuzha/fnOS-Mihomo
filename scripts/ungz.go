package main

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
)

// cmdUngz 解压 gzip 文件。
//
// mihomo 内核在 Release 里就是 .gz 分发的，打包时先解开再塞进 app/bin。
func cmdUngz(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("用法: ungz <源.gz> <目标文件>")
	}
	n, err := ungzFile(args[0], args[1])
	if err != nil {
		return err
	}
	fmt.Printf("  已解压 %s (%d 字节)\n", args[1], n)
	return nil
}

// ungzFile 解压一个 gzip 文件，顺带把权限设成可执行。
//
// 内核解压出来要能直接执行，避免装到 NAS 上还得手工 chmod。
func ungzFile(src, dst string) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	gz, err := gzip.NewReader(in)
	if err != nil {
		return 0, fmt.Errorf("%s 不是有效的 gzip 文件: %w", src, err)
	}
	defer gz.Close()
	out, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, gz)
	if err != nil {
		out.Close()
		return 0, err
	}
	if err := out.Close(); err != nil {
		return 0, err
	}
	if err := os.Chmod(dst, 0o755); err != nil {
		return 0, err
	}
	return n, nil
}
