# AGENTS.md

飞牛 fnOS 上的 Mihomo 本地代理应用：订阅管理、第三方控制面板、NAS 本机出站流量接管，最终打包成 .fpk 安装包。
面向使用者的说明见 README.md，面向改代码打包的说明见 DEVELOPMENT.md。

## 重要约定

- **不要自动提交**。除非用户明确要求，不要执行 `git commit` / `git push` / `git tag`，不要建分支，也不要改写历史。改完代码把改了什么、为什么改、怎么验证说清楚，提交由用户自己决定。
- 不要 `git add -A` 后直接提交。先看 `git status`，确认没有把内核二进制、安装包、`.venv`、含凭据的脚本带进去。

## Setup commands

- 拉依赖：`go mod download`
- 本地跑面板：`go run ./cmd/panel --port 8765 --data ./data --web ./web --bin . --etc .`
- 编译：`go build ./...`
- 打包：`go run ./scripts build [amd64|arm64|all]`（唯一实现，跨平台；没有 ps/sh 包装脚本）
- 测试：`go test ./...`
- 静态检查：`go vet ./...`
- 前端完整性检查：`go run ./scripts check_frontend`
- 下载内核：`go run ./scripts fetch_mihomo`（装到 `assets/`，已存在会跳过）
- 下载第三方面板归档：`go run ./scripts fetch_dashboard`（装到 `assets/`，本地已是最新就跳过）
- 生成 FnDepot 源索引：`go run ./scripts fndepot build --out build/fnpack.json --icon fnos/ICON.PNG --fpk x86=<包> --fpk arm=<包>`
- 校验 FnDepot 源索引：`go run ./scripts fndepot check --file build/fnpack.json`
- 核对 FnDepot 源索引与安装包：`go run ./scripts fndepot verify --file build/fnpack.json --fpk x86=<包> --fpk arm=<包>`

## Project layout

- `cmd/panel/` — 面板服务入口（Go 主程序，REST API + 静态页）
- `internal/sub/` — 订阅解析：Clash YAML / base64 链接列表 → 节点列表
- `internal/mihomo/` — mihomo 配置生成：节点、策略组、分流规则、geodata 路径
- `internal/dashboard/` — 第三方面板：元数据、归档下载、版本探测与安装
- `internal/core/` — mihomo 内核进程的启停与日志采集
- `internal/gateway/` — NAS 本机出站流量接管（TUN 模式）
- `internal/api/` — 面板 REST API（按域拆文件：settings/subscriptions/core/proxies/gateway/dashboard）
- `internal/app/` — 面板业务逻辑：订阅拉取与缓存、配置生成、第三方面板安装
- `internal/settings/` — 设置持久化（config.json，内存语义见 DEVELOPMENT.md）
- `internal/safeio/` — 原子写入：写唯一临时文件再改名，所有落盘都走它
- `internal/fndepot/` — FnDepot 外部源索引（V2）：模型、合并、校验
- `web/` — 面板前端（原生 HTML/CSS/JS，无框架），**前端唯一源文件**
- `fnos/app/www/` — 前端镜像副本（打包输入），改完 `web/` 必须同步到这里
- `fnos/` — fnOS 应用包结构：manifest、wizard、cmd 脚本、ui 配置
- `fndepot/source.json` — 应用源配置：源名称、作者、分类、README 地址（CI 与本地共用）
- `scripts/` — 维护脚本（Go 实现，进版本库）：前端校验、解压、拷贝 geodata、生成图标、下载内核、**打包（`build` 子命令是唯一的打包实现）**
- `scripts/deploy/` — NAS 部署工具（`go run ./scripts deploy <子命令>`）：执行命令、传文件、热更新、检查运行状态
- `build/` — 构建产物：各架构的暂存目录、fnpack 打包器、`.fpk` 成品，不进版本库
- `assets/` — 内核、geodata 与第三方面板归档，体积大，不进版本库

## Code style

- Go：优先标准库；错误信息与日志用中文，和现有代码保持一致。
- Go：注释写「为什么这么写」，不要复述代码在做什么。
- Go：落盘一律走 `internal/safeio.WriteFile`（唯一临时文件 + 改名）。别自己写固定的 `.tmp` —— 并发写会撞名，NAS 上真出过 `rename ... no such file or directory`。
- Go：内核启停只走 `internal/core`：`Restart` 是原子的（停 + 起一把锁，别拆成两步），「内核已经在跑」用 `core.ErrAlreadyRunning` 表达 —— 调用方分「目标已达成」和「真失败」两种态度，别一律当错误。
- 打包产物名带版本号：`Mihomo-<版本>-<架构>.fpk`。FnDepot 规范要求「版本号 + 架构」的包文件视为不可变，同名文件换内容，客户端缓存会让一部分人永远装到旧包。
- `fnpack.json`（FnDepot 外部源索引）只由 `go run ./scripts fndepot` 生成与校验，别手写 —— 客户端对格式很严，JSON 语法错会让整个源同步失败。
- 推源之前必须核对：`go run ./scripts fndepot verify`（索引 ↔ 包内 manifest ↔ Release 资产）。键、版本、架构、摘要、字节数对不上就别推。
- 应用包不进源仓库：包放各自项目的 GitHub Release，源仓库只放索引与图标。放进去 git 历史删不掉，源仓库会迅速胀起来。
- 前端：不引入框架，只用原生 `fetch` 与 DOM API；在 fnOS 桌面的 iframe 里跑得更稳。
- 前端：样式在 `web/app.css` 里（`index.html` 用 `<link>` 引），沿用 GitHub Dark 色板（背景 `#0f1419`、卡片 `#161b22`、边框 `#30363d`、强调 `#1f6feb`）。
- 前端：暗色页面必须声明 `color-scheme: dark`，否则浏览器会画白色滚动条。
- 前端：滚动条是自绘的（`app.js` 的 `attachScrollbar` + `.cs-track`/`.cs-thumb`），原生滚动条已隐藏，别再往里塞 `::-webkit-scrollbar` 规则。
- 前端：响应式分四档 —— 1000px 侧栏收成图标栏、760px 开关与下拉框独占整行、700px 导航挪到顶部横排且表格改成卡片堆叠、480px 表单字段整行一个。改样式时别把这几个断点弄乱。
- 面板端口 8765 写死在三处（`fnos/manifest`、`fnos/app/ui/config`、`fnos/cmd/main`），改端口三处同步。
- 旁路由功能已被移除并有测试守着，不要重新引入。

## Testing instructions

- 单元测试：`go test ./...`（重点在 `internal/sub`、`internal/mihomo`、`internal/api`、`internal/app`、`internal/fndepot`）。
- 涉及并发写的改动要补并发用例：同一个文件被多路同时写时不能报错、不能写出半份内容、不能留残留临时文件。
- 改了前端必须跑 `go run ./scripts check_frontend`：它检查 JS 语法、关键函数是否存在、初始加载调用是否还在、HTML 里的事件处理函数是否都指向已定义函数、`app.css` 是否存在且被引用，以及 `fnos/app/www` 镜像是否与 `web/` 一致。历史上出过两次静默失败（函数被删、页面什么都不渲染），语法检查通过但功能是坏的。
- 新增行为都要补测试，参考同目录已有的 `*_test.go`。
- 提交前至少让 `go test ./...` 与 `go run ./scripts check_frontend` 都通过。

## PR & commit conventions

- 默认分支 `main`；不要直接往 `main` 上推改动。
- **提交动作由用户发起**：不要自动 `git commit` / `git push`。
- 提交信息用中文，格式自定（如「修复：xxx」「新增：xxx」），跟着仓库现有风格走。
- 本仓库暂未配置远端；需要推送时先问用户远端地址与分支策略。

## Security

- 不要把密钥、订阅链接、NAS 密码写进代码或提交进仓库。
- NAS 密码只放在项目根目录的 `.env`（已 gitignore），模板是 `.env.example`。**不要再把凭据写进任何源码** —— 包括 `scripts/deploy/`：它只从 `.env` 读参数，源码里不许出现地址或密码。
- 应用源仓库（`fangfuzha/FnDepot`）的写 token 只进 GitHub Secrets（`FNDEPOT_TOKEN`），源码里只读环境变量。`fndepot publish` 的 token 优先级：`--token` > `FNDEPOT_TOKEN` > `GH_TOKEN` > `GITHUB_TOKEN`。
- 私钥类的东西一律只进 GitHub Secrets，源码与文档里都不许出现内容：`FNPACK_KEY`/`FNPACK_CERT`（fnpack 开发者证书）、`GPG_PRIVATE_KEY`/`GPG_PASSPHRASE`（GPG 私钥与口令）。
- FnDepot 客户端只校验索引里的 `sha256`，协议里没有签名字段：要让用户能验「来源」，靠 fnpack 开发者证书（fnOS 原生）或 GPG 签 `SHA256SUMS`（人工）。CI 的 release.yml 两者都做，缺哪个都还能发。
- 日志里不要打印完整订阅链接（里面有机场 token），必要时脱敏。
- `secret` 字段为空时不能写成 `secret: ""`（yaml.v3 会输出六个单引号），用 `*string` 让该字段不输出。
- 面板口令只存 PBKDF2 哈希（`internal/settings/password.go`），别改成明文落盘。
- 写接口只收 `application/json`：这是防跨站请求伪造的，别放宽。
- 改前端要同时改 `web/` 和 `fnos/app/www/`（后者是打包用的镜像）。
- 仓库有 `.gitattributes`，文本一律 LF —— `fnos/cmd/*` 是 NAS 上执行的 bash 脚本，CRLF 会直接执行失败。
