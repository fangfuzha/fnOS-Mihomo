package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// 前端完整性校验。
//
// 历史上出过两次静默故障，都是 web/app.js 被截断导致函数丢失：
//  1. esc 函数被删 -> 页面报 "esc is not defined"
//  2. 初始加载调用被删 -> 页面什么都不渲染
//
// 这两次语法检查都能过，但功能是坏的。所以光查语法没有意义，
// 真正要查的是：关键函数还在不在、初始加载调用还在不在、
// HTML 里绑定的事件处理函数是不是都指向已定义过的函数。
func cmdCheckFrontend(args []string) error {
	root := repoRoot()
	if len(args) > 0 {
		root = args[0]
	}
	appJS := filepath.Join(root, "web", "app.js")
	indexHTML := filepath.Join(root, "web", "index.html")

	src, err := os.ReadFile(appJS)
	if err != nil {
		return fmt.Errorf("读取 %s 失败: %w", appJS, err)
	}
	html, err := os.ReadFile(indexHTML)
	if err != nil {
		return fmt.Errorf("读取 %s 失败: %w", indexHTML, err)
	}
	js := string(src)
	page := string(html)

	var problems []string

	// 1. 语法：先做自带的括号/字符串配对检查（能抓文件被截断），
	//    本机有 node 的话再补一次 node --check。
	if msg := jsSyntaxError(js); msg != "" {
		problems = append(problems, "app.js 语法可疑: "+msg)
	}
	if node, err := exec.LookPath("node"); err == nil {
		cmd := exec.Command(node, "--check", appJS)
		out, err := cmd.CombinedOutput()
		if err != nil {
			problems = append(problems, "app.js 语法错误:\n"+strings.TrimSpace(string(out)))
		}
	} else {
		fmt.Println("提示: 本机没有 node，跳过 node --check（自带检查已覆盖截断类问题）")
	}

	// 2. 关键函数必须存在，少一个页面就会报错。
	defined := definedFunctions(js)
	for _, fn := range requiredFunctions {
		if !defined[fn] {
			problems = append(problems, "缺少函数定义: "+fn)
		}
	}

	// 3. 初始加载调用必须还在，页面才不会什么都不渲染。
	for _, call := range requiredCalls {
		if !strings.Contains(js, call) {
			problems = append(problems, "缺少初始加载调用: "+call)
		}
	}

	// 4. HTML 里绑定的事件处理函数都必须在 JS 里定义过。
	for _, fn := range eventHandlers(page) {
		if !defined[fn] {
			problems = append(problems, "HTML 引用了未定义的函数: "+fn)
		}
	}

	// 5. 必须存在的元素。
	for _, id := range requiredIDs {
		if !strings.Contains(page, "id=\""+id+"\"") {
			problems = append(problems, "index.html 缺少元素: "+id)
		}
	}

	// 6. 已删除的功能不该有残留。
	for _, gone := range forbiddenWords {
		if strings.Contains(js, gone) {
			problems = append(problems, "app.js 仍残留已废弃的: "+gone)
		}
		if strings.Contains(page, gone) {
			problems = append(problems, "index.html 仍残留已废弃的: "+gone)
		}
	}

	// 7. 打包用的镜像必须和源文件一致，否则装出来的还是旧版前端。
	for _, name := range frontendFiles {
		if !sameFile(filepath.Join(root, "web", name), filepath.Join(root, "fnos", "app", "www", name)) {
			problems = append(problems, "fnos/app/www 与 web 不一致（"+name+"），打包前请把 web/ 同步过去")
		}
	}

	// 8. 样式文件必须在，而且不能空。
	// 少一个样式文件，静态服务会拿 index.html 当 CSS 回落回去，
	// 页面会变得没有样式 —— 而且不会报任何错。
	cssBytes, err := os.ReadFile(filepath.Join(root, "web", "app.css"))
	if err != nil {
		problems = append(problems, "缺少 web/app.css: "+err.Error())
	} else if len(cssBytes) == 0 {
		problems = append(problems, "web/app.css 是空的")
	}

	// 9. 页面必须引用样式文件。
	if !strings.Contains(page, `href="/app.css"`) {
		problems = append(problems, "index.html 没有引用 /app.css")
	}

	if len(problems) > 0 {
		fmt.Println("前端检查未通过:")
		for _, p := range problems {
			fmt.Println("  - " + p)
		}
		return fmt.Errorf("前端检查未通过")
	}
	fmt.Println("前端检查通过：语法正确、函数齐全、页面引用无误、无残留")
	return nil
}

// 关键函数：少一个页面就会在浏览器里报错。
var requiredFunctions = []string{
	"api", "toast", "esc", "loadStatus", "loadSubs", "loadSettings",
	"loadGateway", "saveGateway", "loadDashboard", "saveDashboard",
	"openDashExternal", "openDashIPv6", "renderLogs",
	"coreAction", "saveSub", "refreshSub", "toggleSub", "delSub",
	"loadLogs", "saveSettings",
	// 登录：设了面板口令后前端要靠这几个函数挡住未登录的界面。
	"boot", "startApp", "showLogin", "hideLogin", "doLogin", "doLogout",
	"clearPanelPassword", "refreshAuth",
}

// 首次进入页面必须跑起来的调用。
var requiredCalls = []string{
	"loadStatus();", "loadSubs();", "loadSettings();", "loadGateway();", "loadDashboard();",
}

// 页面结构里必须存在的元素 id。
var requiredIDs = []string{
	"dash-select", "dash-hint", "dash-info", "dash-ipv6-btn",
	"log-body", "log-level", "log-lines", "log-scroll", "sub-scroll",
	// 登录页与面板访问设置。
	"login-screen", "login-user", "login-pass", "login-msg",
	"set-user", "set-passwd", "set-auth-state",
}

// 已经废弃、不该再出现的东西。
var forbiddenWords = []string{"loadProxies", "switchProxy", "旁路由", "dash-frame"}

var funcRe = regexp.MustCompile(`(?:async\s+)?function\s+([A-Za-z_][A-Za-z0-9_]*)`)
var handlerRe = regexp.MustCompile(`on(?:click|change|input)="([A-Za-z_][A-Za-z0-9_]*)\(`)

func definedFunctions(js string) map[string]bool {
	out := map[string]bool{}
	for _, m := range funcRe.FindAllStringSubmatch(js, -1) {
		out[m[1]] = true
	}
	return out
}

func eventHandlers(html string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range handlerRe.FindAllStringSubmatch(html, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// sameFile 比较两个文件的内容是否一致。
func sameFile(a, b string) bool {
	ha, err := fileHash(a)
	if err != nil {
		return false
	}
	hb, err := fileHash(b)
	if err != nil {
		return false
	}
	return ha == hb
}

func fileHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// repoRoot 从当前目录往上找 go.mod，定位仓库根目录。
// 这样在任意子目录里执行都能找到 web/ 与 fnos/。
func repoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

// jsSyntaxError 做一次轻量的括号与字符串配对检查。
//
// 不做完整解析，只抓最常见的一种坏法：文件被截断。
// 截断会留下没配平的括号或没闭合的字符串，
// 有 node 时 node --check 更准，没有 node 时至少还能兜个底。
func jsSyntaxError(src string) string {
	runes := []rune(src)
	pos, msg := scanCode(runes, 0, 0)
	if msg != "" {
		return fmt.Sprintf("第 %d 行: %s", lineAt(runes, pos), msg)
	}
	return ""
}

// scanCode 从 start 起扫描一段 JS 代码。
//
// stopChar 为 0 表示扫到文件末尾；不为 0 表示扫到该字符为止，
// 模板字符串的插值部分就是这么递归进来的。
func scanCode(runes []rune, start int, stopChar rune) (int, string) {
	var stack []rune
	prev := rune(0) // 上一个有效字符，用来区分除号和正则开头
	for i := start; i < len(runes); i++ {
		c := runes[i]
		if c == '\n' {
			continue
		}
		if c == ' ' || c == '\t' || c == '\r' {
			continue
		}
		// 行注释
		if c == '/' && i+1 < len(runes) && runes[i+1] == '/' {
			for i < len(runes) && runes[i] != '\n' {
				i++
			}
			continue
		}
		// 块注释
		if c == '/' && i+1 < len(runes) && runes[i+1] == '*' {
			for i += 2; i < len(runes); i++ {
				if runes[i] == '*' && i+1 < len(runes) && runes[i+1] == '/' {
					i++
					break
				}
			}
			continue
		}
		// 字符串
		if c == '\'' || c == '"' {
			end, msg := scanQuoted(runes, i, c)
			if msg != "" {
				return i, msg
			}
			i = end
			prev = c
			continue
		}
		// 模板字符串
		if c == '`' {
			end, msg := scanTemplate(runes, i)
			if msg != "" {
				return i, msg
			}
			i = end
			prev = c
			continue
		}
		// 正则字面量：前一个字符不是值的结尾时，斜杠开头就是正则
		if c == '/' && !isValueEnd(prev) {
			end, msg := scanRegex(runes, i)
			if msg != "" {
				return i, msg
			}
			i = end
			prev = '/'
			continue
		}
		switch c {
		case '{', '(', '[':
			stack = append(stack, c)
		case '}', ')', ']':
			// 插值表达式到这里结束
			if stopChar != 0 && c == stopChar && len(stack) == 0 {
				return i, ""
			}
			if len(stack) == 0 {
				return i, fmt.Sprintf("多出一个 %q", string(c))
			}
			open := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if !bracketMatch(open, c) {
				return i, fmt.Sprintf("%q 和 %q 不配对", string(open), string(c))
			}
		}
		prev = c
	}
	if len(stack) > 0 {
		return len(runes), fmt.Sprintf("文件结尾还有 %d 处未闭合的括号", len(stack))
	}
	if stopChar != 0 {
		return len(runes), fmt.Sprintf("缺少 %q", string(stopChar))
	}
	return len(runes), ""
}

// scanQuoted 扫描引号字符串，返回结束引号的位置。
func scanQuoted(runes []rune, start int, quote rune) (int, string) {
	for i := start + 1; i < len(runes); i++ {
		switch runes[i] {
		case '\\':
			i++
		case quote:
			return i, ""
		case '\n':
			return i, "字符串跨行未闭合"
		}
	}
	return len(runes) - 1, "字符串没有闭合"
}

// scanTemplate 扫描模板字符串。
//
// 插值 ${...} 里还能再套模板字符串，所以插值部分要递归扫一遍表达式，
// 否则会把插值里的 </div> 当成正则开头。
func scanTemplate(runes []rune, start int) (int, string) {
	for i := start + 1; i < len(runes); i++ {
		switch runes[i] {
		case '\\':
			i++
		case '`':
			return i, ""
		case '$':
			if i+1 < len(runes) && runes[i+1] == '{' {
				end, msg := scanCode(runes, i+2, '}')
				if msg != "" {
					return i, msg
				}
				i = end
			}
		}
	}
	return len(runes) - 1, "模板字符串没有闭合"
}

// scanRegex 扫描正则字面量，字符类 [ ] 里的斜杠不算结束。
func scanRegex(runes []rune, start int) (int, string) {
	inClass := false
	for i := start + 1; i < len(runes); i++ {
		switch runes[i] {
		case '\\':
			i++
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '/':
			if !inClass {
				return i, ""
			}
		case '\n':
			return i, "正则字面量没有闭合"
		}
	}
	return len(runes) - 1, "正则字面量没有闭合"
}

// isValueEnd 判断前一个字符是否让斜杠被当成除号。
// 字符串、模板串、数字、标识符结尾都算值，除号跟在值后面才是除法。
func isValueEnd(c rune) bool {
	switch c {
	case 0:
		return false
	case ')', ']', '}', '/', '\'', '"', '`':
		return true
	}
	return unicode.IsLetter(c) || unicode.IsDigit(c) || c == '_' || c == '$'
}

// lineAt 返回第 idx 个字符所在行号，用于把错误定位到具体行。
func lineAt(runes []rune, idx int) int {
	line := 1
	for i := 0; i < idx && i < len(runes); i++ {
		if runes[i] == '\n' {
			line++
		}
	}
	return line
}

func bracketMatch(open, close rune) bool {
	switch open {
	case '{':
		return close == '}'
	case '(':
		return close == ')'
	case '[':
		return close == ']'
	}
	return false
}
