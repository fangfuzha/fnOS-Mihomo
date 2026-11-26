package api

import (
	"net/http"
	"os"
	"path/filepath"
)

// StaticHandler 提供面板静态页面；找不到文件时回落到 index.html。
//
// 前端是单页应用，路由由浏览器端处理，
// 所以任何没匹配到文件的路径都回落到 index.html。
func StaticHandler(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(dir, filepath.Clean(r.URL.Path))
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			fs.ServeHTTP(w, r)
			return
		}
		index := filepath.Join(dir, "index.html")
		if _, err := os.Stat(index); err == nil {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, index)
			return
		}
		http.Error(w, "页面资源未找到", http.StatusNotFound)
	})
}
