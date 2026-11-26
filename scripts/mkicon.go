package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

// cmdMkicon 生成 fnOS 桌面图标（64 与 256 两份）。
//
// 图标是画出来的：深蓝到青绿的渐变圆底，中间三段圆环像代理节点连线。
// 直接逐像素算再用 image/png 编码，不需要任何图像库。
func cmdMkicon(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("用法: mkicon <64.png> <256.png>")
	}
	sizes := []int{64, 256}
	for i, path := range args {
		if err := writeIcon(path, sizes[i]); err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		fmt.Printf("  已生成 %s (%dx%d, %d 字节)\n", path, sizes[i], sizes[i], info.Size())
	}
	return nil
}

// writeIcon 画一个图标并写成 PNG。
//
// 不打印任何东西 —— 这个函数也被 fetch_fnpack 拿去生成探针包的图标，
// 那时候的输出是噪音。
func writeIcon(path string, size int) error {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	center := float64(size-1) / 2
	outer := float64(size) * 0.47
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := float64(x)-center, float64(y)-center
			d := math.Hypot(dx, dy)

			// 圆形外缘做一像素抗锯齿
			alpha := 0
			if d <= outer {
				alpha = 255
			}
			if outer-1 < d && d <= outer {
				alpha = int(255 * (outer - d))
			}

			// 深蓝 -> 青绿 渐变
			t := float64(x+y) / (2 * float64(size))
			r := int(24 + 30*t)
			g := int(60 + 150*t)
			b := int(120 + 120*t)

			// 中心三段圆环，均匀分布在圆周上
			ang := math.Atan2(dy, dx)
			rr := d / (float64(size) * 0.30)
			if rr > 0.55 && rr < 1.0 {
				seg := math.Mod(ang+math.Pi, 2*math.Pi/3)
				if seg < 1.1 {
					r, g, b = 240, 250, 255
				}
			}
			img.SetRGBA(x, y, color.RGBA{uint8(r), uint8(g), uint8(b), uint8(alpha)})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		return err
	}
	return nil
}
