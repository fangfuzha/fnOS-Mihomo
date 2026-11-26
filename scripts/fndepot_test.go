package main

import "testing"

// readme_url 必须指向 raw 的 Markdown 原文，不能是 GitHub 网页版地址：
// 客户端把抓到的内容当 Markdown 渲染，blob 页面会把整页 HTML
// （导航、页脚）一起带进来，详情页一塌糊涂。
func TestDeriveReadmeURL(t *testing.T) {
	cases := map[string]string{
		"https://github.com/fangfuzha/fnOS-Mihomo":  "https://raw.githubusercontent.com/fangfuzha/fnOS-Mihomo/main/README.md",
		"https://github.com/fangfuzha/fnOS-Mihomo/": "https://raw.githubusercontent.com/fangfuzha/fnOS-Mihomo/main/README.md",
	}
	for in, want := range cases {
		if got := deriveReadmeURL(in); got != want {
			t.Errorf("deriveReadmeURL(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestDeriveBugURL(t *testing.T) {
	got := deriveBugURL("https://github.com/fangfuzha/fnOS-Mihomo/")
	want := "https://github.com/fangfuzha/fnOS-Mihomo/issues"
	if got != want {
		t.Fatalf("deriveBugURL = %q，期望 %q", got, want)
	}
}
