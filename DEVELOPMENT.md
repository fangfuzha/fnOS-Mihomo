# 开发说明

本文件面向要改代码、重新打包的人。面向使用者的说明见 [README.md](README.md)。

## 项目结构

```
cmd/panel/           面板服务入口（Go，主程序；只做装配与启停）
internal/sub/        订阅解析：Clash YAML / base64 链接列表 → 节点列表
internal/mihomo/     mihomo 配置生成：节点、策略组、分流规则、geodata 路径
internal/dashboard/  第三方面板：元数据、归档下载、版本探测与安装
internal/core/       mihomo 内核进程的启停与日志采集
internal/gateway/    NAS 本机出站流量接管（TUN 模式）：启停、验证、失败自动撤销
internal/api/        面板 REST API（按域拆文件）
internal/app/        面板业务逻辑：订阅拉取与缓存、配置生成、第三方面板安装
internal/settings/   设置的持久化
internal/safeio/    原子写入：唯一临时文件 + 改名，落盘一律走它
internal/fndepot/   FnDepot 外部源索引：V2 模型、合并、校验
web/                 面板前端（原生 HTML/CSS/JS，无框架）
fnos/                fnOS 应用包结构（打包输入）
fndepot/source.json   应用源配置：源名称、作者、分类、README 地址
scripts/             维护脚本（Go 实现）：前端校验、解压、拷贝 geodata、图标、下载内核、打包
scripts/deploy/      NAS 部署工具：go run ./scripts deploy <子命令>
assets/              mihomo 内核、geodata 与第三方面板归档（不进版本库）
build/               构建产物：暂存目录、打包器、.fpk（不进版本库）
```

## 构建

```bash
# 1. 下载 mihomo 内核（双架构）
go run ./scripts fetch_mihomo

# 2. 下载 geodata（国内 NAS 连不上 GitHub releases，必须随包分发）
#    geoip.dat / geoip.metadb / geosite.dat / country.mmdb
go run ./scripts fetch_geodata

# 3. 下载第三方面板归档（同样随包分发，NAS 上装好就能用）
go run ./scripts fetch_dashboard

# 4. 打包（跨平台，Windows / Linux / macOS 都是这一条命令）
go run ./scripts build amd64   # x86
go run ./scripts build arm64   # ARM
go run ./scripts build all     # 两个都打
```

产物：`build/Mihomo-<版本>-x86.fpk`、`build/Mihomo-<版本>-arm64.fpk`（都落在 `build/` 下，仓库根目录只留源码）。

产物名带版本号是 FnDepot 外部源的要求，见下面的「FnDepot 外部源」。

### 行尾

仓库里有 `.gitattributes`，所有文本一律以 LF 存取。

`fnos/cmd/*` 是要拿到 NAS 上用 bash 执行的脚本，被换成 CRLF 会直接执行失败 ——
`\r` 变成命令的一部分，报 `command not found`。
只靠每个人本机的 `core.autocrlf` 靠不住：Windows 上默认就是 `true`。

## 打包工作流（CI）

两个工作流文件，各自独立触发：

| 文件 | 触发 | 做什么 |
|---|---|---|
| `.github/workflows/ci.yml` | `pull_request`、push 到 `main`、手动 | 检查（双平台）+ 打包出两个 `.fpk` 作为 artifact |
| `.github/workflows/release.yml` | 打 `v*` tag、手动 | 打包 + 发 GitHub Release |

`ci.yml` 里的 `check` job 在 **windows-latest 与 ubuntu-latest 两个平台**各跑一遍：
`go vet`、`go test ./...`、前端完整性检查；`build` job 依赖它通过。

**为什么检查要跑两个平台**：以前的 `build.sh` 漏了架构校验、前端镜像同步，
Windows 上却好好的 —— 这类差异只有两个平台都跑才会暴露出来。
现在打包流程只有一份实现（`scripts/build.go`），但两个平台仍要各自验证一遍。

**为什么发版是单独一个文件**：发版只在打 tag 时发生，触发条件与检查完全不同，
本就该是两条独立的链。`release.yml` 自己打包自己发版，
不用把 artifact 从一个工作流传到另一个（上一版拆成独立 job 时就是这么绕的，
多一次「传下来再传上去」纯属白折腾）。写权限也只给发版那个 job。

fnpack 是官方提供的第三方二进制，**按运行平台取对应的那一份**：
本地 Windows 上是 `fnpack-1.2.3-windows-amd64`，CI 的 Linux runner 上是
`fnpack-1.2.3-linux-amd64`（两份都在 NAS 上实测过能正常出包）。
`scripts build` 自动下载到 `build/fnpack/`，版本固定 1.2.3
（见下面的「打包器的版本」）。它不进版本库，所以仓库里也没有 `third_party/` 目录。

CI 的打包 job 跑在 **ubuntu-latest** —— Linux runner 比 Windows 快一半左右，
顺带把 Linux 上的打包路径也覆盖了（那份以前最容易出问题）。

> `scripts/deploy`（NAS 部署工具）的测试**在 CI 里跑**：它跟主模块在一起，
> `go test ./...` 会连它一起测 —— 其中有一个进程内的 SSH 服务端当 NAS 的替身。

### 缓存

三个缓存，键都带内容哈希，改了内容就自动失效：

| 缓存 | 路径 | 键 | 大头 |
|---|---|---|---|
| Go 依赖与编译缓存 | `setup-go` 自己管 | `go.sum` | 依赖与编译产物 |
| 打包资源 | `assets/` | 版本变量 + 下载脚本哈希 + **日期** | 约 90 MB（内核/geodata/第三方面板） |
| fnpack 打包器 | `build/fnpack/` | `runner.os` + `fetchfnpack.go` 哈希 | 4 MB |

**打包资源为什么按天分桶**：geodata 与第三方面板跟的是 `latest`，
而下载脚本是「文件已存在就跳过」。缓存键一旦钉死，geodata 就会永远停在
第一次缓存的那个版本上，latest 名存实亡。按天分桶后，
一天内重复跑直接复用（省掉 90 MB 下载），隔天自动重新拉一次。

两个工作流用**同一个缓存键**，同一天的检查与发版能共用一份缓存。

`ci.yml` 还设了 `concurrency`：同一分支来了新提交，就把旧的跑停 ——
省下来的都是 CI 分钟。

geodata 全部来自 [MetaCubeX/meta-rules-dat](https://github.com/MetaCubeX/meta-rules-dat)
的 `releases/latest/download/`（mihomo 自己自动更新 geodata 时默认也是这个源）。

### 签名

fnpack 1.3 起打包默认要开发者证书，没配证书时用 `--nosign` ——
自用安装没问题，但**未签名的包不能上架应用中心**。

CI 里配两个仓库密钥就能签出可上架的包：

| 密钥 | 用途 |
|---|---|
| `FNPACK_KEY` | 开发者私钥（`fnpack cert import` 的 key） |
| `FNPACK_CERT` | 开发者证书（`fnpack cert import` 的 crt） |

> 1.2.3 不强制要求证书，没配证书直接就能打；配了才签。
> 1.3.x 反过来 —— 默认要证书，没配就用 `--nosign`。

### 本地打包

打包流程只有一份实现，在 `scripts/build.go` 里（Go，跨平台）：

```bash
go run ./scripts build amd64   # x86
go run ./scripts build arm64   # ARM
go run ./scripts build all     # 两个都打
```

没有平台专用的包装脚本。以前的 `build.ps1` 与 `build.sh` 各写一遍同样的步骤，
改一处漏一处是迟早的事 —— Linux 那份就漏了架构校验和前端镜像同步，
两边行为还不一样。现在一份实现，Windows / Linux / macOS 走同一条路。

流程按架构在 `build/<arch>/` 里独立暂存，**不改源码目录** ——
`fnos/manifest` 的 `platform` 字段是在暂存副本上改的。
两个架构共用一个目录会让后一次构建覆盖前一次的二进制，
打出来的包里装着另一个架构的可执行文件，装到 NAS 上表现为 "ELF: not found"。


## 打包器的版本

打包器统一用 **fnpack 1.2.3**，版本写死在 `scripts/fetchfnpack.go` 的
`fnpackVersion` 常量里。`scripts fetch_fnpack` 按需从官方地址下载当前平台
对应的那一份到 `build/fnpack/`，并拿一个最小包试打确认能用 ——
fnpack 打包失败时也返回 0，只有真的产出 .fpk 才算成功。

**为什么不用上游最新版（1.3.x）**：1.3.x 的官方 Windows 构建有缺陷 ——
它对只读句柄调 fsync，而 Windows 的 `FlushFileBuffers` 要求句柄可写，
于是报 `sync xxx.fpk-123: Access is denied`，在这台机器上打不出包。

这不是我们包结构的问题，也不是杀软拦截（杀软日志里没有任何拦截记录，
干净目录、不同盘符下都复现）。已确认是官方 **Windows 构建**自己的问题：
同一个 1.3.3 的 `linux-amd64` 构建，在 Linux 上打**同一个包目录**一次就过
（2026-11-25 实测，两个包的顶层结构完全一致）。

所以暂时统一用 1.2.3 —— Windows 和 Linux 上都能正常出包。

**想换版本时**：改 `fnpackVersion` 常量，在 `fnpackSHA256` 里补上对应平台的
摘要，然后在目标平台跑一次 `go run ./scripts fetch_fnpack`：它会拿最小包试打，
打不出包就直接报错，不会把坏工具留在机器上。

确实需要 1.3.x 打的时候，在 Linux 上打就行（NAS 本来就是 Linux）：

```bash
# 1. 本机只做暂存：编译面板、解压内核、同步 geodata 与第三方面板、前端镜像。
#    这一步会顺带用 1.2.3 打一个包，那份留着当对照。
go run ./scripts build amd64

# 2. 把暂存目录传上去
tar --exclude='*.fpk' -cf build/amd64_stage.tar -C build/amd64 .
go run ./scripts deploy put -dest /tmp/amd64_stage.tar build/amd64_stage.tar

# 3. 传 linux 版 fnpack 上去
curl -Lo fnpack-linux https://static2.fnnas.com/fnpack/fnpack-1.3.3-linux-amd64
go run ./scripts deploy put -dest /tmp/fnpack-linux ./fnpack-linux

# 4. 在 NAS 上打包（NAS 是 x86_64，用 linux-amd64 那份）
go run ./scripts deploy exec -c "set -e; rm -rf /tmp/mihomo-build; mkdir -p /tmp/mihomo-build;
cd /tmp/mihomo-build; tar -xf /tmp/amd64_stage.tar; chmod +x /tmp/fnpack-linux;
/tmp/fnpack-linux build --nosign --directory ."

# 5. 把包拉回来
go run ./scripts deploy get -src /tmp/mihomo-build/Mihomo.fpk ./build/Mihomo-<版本>-x86.fpk
```

> `get` 是给部署工具补的子命令（与 `put` 反向）。
> 原来只有 `put`，没有它包就只能留在 NAS 上。

两点注意：

- 1.3.3 默认要开发者证书，没配就 `--nosign`（自用安装没问题，不能上架应用中心）。
- `fnpack build` 失败时也**返回 0**，只能看有没有真的产出 .fpk。

arm64 没有 ARM 版的 Linux 主机可用（NAS 是 x86_64，也没有 qemu-user），
所以 arm64 只能在本机打。

## 维护脚本

`scripts/` 下是一个 Go 程序（打包时叫 `fnos-mihomo-tools`），只用标准库，
Windows / Linux / macOS 都能跑，打包机上不必再装 Python。

```bash
go run ./scripts <子命令>              # 直接跑
go build -o build/tools/fnos-mihomo-tools ./scripts   # 先编译再调用
```

| 子命令 | 作用 |
|---|---|
| `check_frontend` | 校验前端资源：JS 语法、关键函数、初始加载调用、页面事件引用、样式文件存在且被引用、`web/` 与 `fnos/app/www` 镜像是否一致 |
| `copyfiles <源> <目标>` | 拷贝 geoip.dat / geoip.metadb / geosite.dat / country.mmdb |
| `copy_dashboard <源> <目标>` | 拷贝第三方面板归档与版本清单 |
| `ungz <源.gz> <目标>` | 解压 mihomo 内核压缩包 |
| `mkicon <64.png> <256.png>` | 生成 fnOS 桌面图标 |
| `fetch_mihomo` | 下载 mihomo 内核（双架构）到 `assets/`，`MIHOMO_VERSION` 可指定版本 |
| `fetch_dashboard` | 下载第三方面板归档到 `assets/`，本地已是最新就跳过 |
| `fetch_geodata` | 下载 geodata 到 `assets/geodata/`，已存在就跳过（`GEODATA_VERSION` 可固定版本） |
| `fetch_fnpack` | 下载 fnpack 打包器到 `build/fnpack/`（默认 1.2.3，`FNPACK_VERSION` 可指定），并拿最小包试打确认能用 |
| `build [amd64\|arm64\|all]` | 打包出 `.fpk`：暂存、交叉编译面板、解压内核、同步资源、前端校验、打包 |
| `fndepot build\|publish\|check\|verify` | FnDepot 外部源索引：生成/推送 `fnpack.json`、按 V2 规范校验、三层核对（索引 ↔ 包 ↔ Release） |

`check_frontend` 是打包前的关卡。历史上出过两次「语法检查通过但页面是坏的」
（`app.js` 被截断导致函数丢失），所以它不只查语法，还查关键函数在不在、
初始加载调用还在不在、HTML 里的事件处理函数有没有指向没定义的函数。
没有 node 时它用自带的扫描器兜底（能抓出截断留下的未闭合括号与字符串）。

样式单独放在 `web/app.css`，所以检查里也带了两条：`app.css` 必须存在且不能空，
`index.html` 必须引用它。少一个样式文件，静态服务会拿 `index.html` 当 CSS 回落回去，
页面会变得没有样式 —— 而且不会报任何错，只能靠检查拦。

### 打包的几个坑

- **每个架构必须在独立目录构建**。共用一个目录时，后一次构建会覆盖前一次的二进制，
  打出��包里可能装着另一个架构的可执行文件 —— 在 NAS 上表现为 `ELF: not found`
  或 `Exec format error`。`scripts build` 用 `build/<arch>/` 暂存目录解决这一点，
  并在打包前校验 ELF 的 e_machine 字段。
- **只对文本文件规整行尾**。把编译产物喂给 `sed -i "s/\r$//"` 会把 ELF 里的
  `0D 0A` 字节对当成行尾删掉，二进制当场损坏。
- **fnpack 要求 `manifest` 的 `desktop_applaunchname` 与 `app/ui/config` 的 key 一致**，
  否则打包直接失败。
- **fnOS 应用包的目录要求**：`app/` 会被映射成安装后的 `target/`（可执行文件目录），
  所以 `ui/` 必须放在 `app/ui/` 下，不能放在包根目录。
- **向导字段名即环境变量名**：`wizard/install` 里的 `field: "wizard_xxx"`
  在 `cmd/install_callback` 里就是 `$wizard_xxx`。
- **向导的 `type` 只支持 `text` / `password` / `radio` / `checkbox` / `select` /
  `switch` / `tips`**，没有 `number`。端口用 `text` + `initValue`。

## FnDepot 外部源

[FnDepot](https://github.com/EWEDLCM/FnDepot) 是飞牛 NAS 上的第三方应用管理器。
它不认 GitHub Release，也不看 tag —— **它只读应用源仓库根目录的 §fnpack.json§**：
按设备架构和系统版本过滤后，选 §releases§ 里版本号最高的那一版，
再和 NAS 上已安装包的 manifest 版本比较来提示升级。

所以「发了新版本客户端就知道」的前提是：**版本号必须写进 §fnpack.json§**。
只推 Release 不动索引，客户端永远看不到新版。

本项目的应用源仓库是 [§fangfuzha/FnDepot§](https://github.com/fangfuzha/FnDepot)，
索引由 §scripts fndepot§ 生成与推送，不手写。

### 一次发版发生了什么

§§§text
打 tag v1.1.0
   └─ release.yml：打包 → build/Mihomo-1.1.0-x86.fpk / -arm64.fpk
                  → 发 GitHub Release
                  → fndepot publish：把 1.1.0 写进源仓库的 fnpack.json
§§§

顺序不能反：索引里的下载地址要先能下得到，客户端才会去装。

### 手动生成与校验

§§§bash
# 生成（写到 build/fnpack.json）
go run ./scripts fndepot build \
  --out build/fnpack.json \
  --icon fnos/ICON.PNG \
  --fpk x86=build/Mihomo-1.1.0-x86.fpk \
  --fpk arm=build/Mihomo-1.1.0-arm64.fpk

# 按 V2 规范校验
go run ./scripts fndepot check --file build/fnpack.json

# 推到应用源仓库（需要 token）
FNDEPOT_TOKEN=xxx go run ./scripts fndepot publish --fpk ... --fpk ...
§§§

源元数据（源名称、作者、分类、README 地址）在 §fndepot/source.json§ 里，
CI 与本地读同一份；命令行参数优先级高于配置文件。

### 验证：推之前先核对

```text
索引        包内 manifest        Release 资产
  |              |                   |
  +-- appname/version/platform       |
  +-- sha256 / size ------------------+
```

三层有两头，任何一头对不上，用户看到的都是坏东西：

- **索引 ↔ 包内 manifest**：应用键、版本、架构。键不符，客户端会直接跳过这个应用；
  版本不符，装上去的是另一个版本，升级判断全乱。
- **索引 ↔ 本地包**：sha256 与字节数。客户端强制校验摘要，对不上就是装失败。
- **索引 ↔ Release 资产**：资产摘要与大小。用 GitHub API 直接查（它给 digest 与 size），
  不用把 50MB 的包下载下来。
- **图标**：得是真 PNG，且不超过 500KB。

```bash
go run ./scripts fndepot verify \
  --file build/fnpack.json \
  --icon fnos/ICON.PNG \
  --fpk x86=build/Mihomo-1.0.0-x86.fpk \
  --fpk arm=build/Mihomo-1.0.0-arm64.fpk \
  --check-release \
  --token `$GITHUB_TOKEN`
```

`release.yml` 里「核对索引与 Release」这一步就是它：对不上直接让发版失败，
源仓库不会被写坏。CI 的检查工作流里跑的是前两层（那时还没有 Release 可查）。

### 提交策略：源仓库会不会胀

**包不进源仓库。** 安装包本体放在应用仓的 GitHub Release 上，源仓库只放索引与图标。
这是源仓库不胀的根本原因 —— 包放进去的话一次就是 50MB，
而且 git 历史删不掉，几轮发版就上百 MB。

**一次发布一个提交。** 索引与图标进同一个 commit（Git Data API：
blobs → tree → commit → ref），不是两次写入两个提交。

**没变就不提交。** 写之前先比 blob sha：图标每次发版都一样，
同一版本的内容也一样，一样就跳过。没有变化时整个发布不产生任何提交。

**同一版本重发时发布时间不变。** 包没变就别改 `updated_at`，
否则内容每次都不同，提交历史会平白多一倍。

实测：源仓库 7 个提交、9 KB。每发一版大约 +1 个提交、+3 KB；
发 100 版也就几百 KB，这个量级不用担心。
### 签名与验证：三条不同的路

| 层 | 手段 | 谁来验 | 配在哪 |
|---|---|---|---|
| 完整性（客户端自动） | 索引里的 `sha256` + `size` | FnDepot 客户端，强制校验 | 自动生成，不用配 |
| 来源可信（fnOS 原生） | fnpack 开发者证书签名 | fnOS 应用中心 | Secrets：`FNPACK_KEY`、`FNPACK_CERT` |
| 来源可信（人工） | GPG 签 `SHA256SUMS` | 人，`gpg --verify` | Secrets：`GPG_PRIVATE_KEY`、`GPG_PASSPHRASE`；变量：`GPG_SIGNING_KEY` |

**客户端能自动验的只有 sha256。** FnDepot 外部源 V2 的 `packages` 字段里
只有 `download_url`、`sha256`、`size`，没有签名字段 ——
客户端不会验任何签名。想要「这个包确实是你发的」，得靠下面两条。

**fnOS 的开发者证书签名。** fnpack 1.3.x 起支持：
``fnpack build --key developer.key --cert developer.crt`。
1.2.x 不支持签名（本地 Windows 用的就是它）。CI 跑在 Linux 上，
用 `FNPACK_VERSION=1.3.3` 就能出签名包 —— 1.3.x 的 Windows 构建有缺陷，
但那个缺陷只在 Windows 上。

证书要向飞牛官方申请（`fnpack cert import -key ./developer.key -crt ./developer.crt`）。
没配证书时 1.3.3 会先试普通打包（失败），再自动退回 `--nosign` 出未签名包 —— 流程不会断。

**GPG 签名。** CI 会把两个包的 SHA256 写成 `SHA256SUMS`，
配了 `GPG_PRIVATE_KEY` 就再签一份 `SHA256SUMS.asc` 一起挂到 Release 上：

```bash
gpg --verify SHA256SUMS.asc
sha256sum -c SHA256SUMS
```

本地打 tag 时也可以 `git tag -s v1.1.0`（签名 tag），
这样「这个 tag 是你打的」也有据可查。

> 签 `SHA256SUMS` 而不是直接签 50MB 的包：摘要文件小得多，验起来也快；
> 索引里的 sha256 与这个文件是同一份数字。
### 仓库密钥

| 密钥 | 用途 |
|---|---|
| §FNDEPOT_TOKEN§ | 往应用源仓库写 §fnpack.json§ 与图标。**没配就跳过这一步**，Release 照发。 |

token 要的是对 §fangfuzha/FnDepot§ 有 contents 写权限的细粒度 PAT。
它只进 GitHub Secrets，不进仓库。

### 几条硬规矩

- **产物名带版本号**（§Mihomo-<版本>-<架构>.fpk§）。规范要求「版本号 + 架构」的包文件
  视为不可变：同名文件换内容，客户端缓存会让一部分人永远装到旧包。
- **同一个版本号不许换内容**。§fndepot publish§ 遇到同版本但 sha256 不一样会直接报错，
  要改包就升版本号。
- **manifest 的 §version§ 与 tag 要一致**。产物名是按 manifest 版本拼的，
  §release.yml§ 里有一步专门核对，对不上直接失败。
- **§fnpack.json§ 不要手写**。字段多而且客户端对格式很严（JSON 语法错会让整个源同步失败），
  一律用 §scripts fndepot§ 生成。
## NAS 部署（scripts deploy）

`scripts/deploy/` 是 NAS 部署工具，用来把面板和安装包推到 NAS 上。
它在主模块里（`fnos-mihomo/scripts/deploy`），所有维护命令共用一个入口：

```bash
go run ./scripts deploy help
```

它依赖 `golang.org/x/crypto/ssh` 与 `github.com/pkg/sftp` ——
面板程序本身不引用它们（`cmd/panel` 不会链进去），只是主模块的 `go.mod` 多了两条 require。

### 参数从哪来

**全部参数在本地参数文件里**，用 dotenv 的约定：文件叫 `.env`、放在**项目根目录**，
模板是同目录的 `.env.example`。先复制一份再改：

```bash
cp .env.example .env
```

优先级是「进程环境变量 > 参数文件」，临时换一台机器试一下可以直接改环境变量。
本地路径（`PANEL_BIN`/`WEB_DIR`/`FPK_FILE`）相对 `REPO_ROOT` 解析，
而 `REPO_ROOT` 默认就是 `.env` 所在的项目根目录 —— 换机器通常不用改。

### 子命令

| 子命令 | 作用 | 对应原来的脚本 |
|---|---|---|
| `exec -c "cmd"` / `exec -f cmds.json` | 在 NAS 上执行命令，JSON 清单支持 `sudo` 字段 | `ssh6.py` |
| `put <本地> [-dest 远端] [-sudo]` | 上传文件 | `upload_fpk.py` 的一部分 |
| `hot-update [--skip-web] [--no-restart]` | 热更新面板二进制与前端；启停走 fnOS 的 `appcenter-cli`，内核会跟着重启 | `hot_update.py` + `round_dash.py` 的前半段 |
| `fpk` | 上传 .fpk 并解包检查 manifest 与目录结构 | `upload_fpk.py` + `verify_fpk.py` |
| `verify [-wait 秒]` | 只读体检：面板状态、内核控制口、面板目录、配置、日志、进程 | `round_dash.py` 的检查部分 |
| `hostkey` | 打印 NAS 的 host key，用来写 known_hosts | 新增 |

### 第一次用

```bash
# 1. 取主机密钥（这一步不做校验，目的正是先把密钥记下来）
go run ./scripts deploy hostkey
#    把打印出来的那行追加到 .env 里 NAS_KNOWN_HOSTS 指向的那个文件

# 2. 之后就不用 -insecure 了
go run ./scripts deploy verify
```

### 热更新的启停必须走 fnOS

fnOS 的应用管理器（`appcenter-cli`）只认**自己启动的进程**。
自己 `pkill` 掉面板、再用 `setsid` 拉一个新的，是绕过它 —— 实测踩过一遍：

1. 飞牛桌面里应用变成「禁用」：fnOS 只看到自己启动的进程没了，
   不知道外面又起了一个新的；
2. 那个野进程还占着 8765。用户点「启用」时 fnOS 先做端口预检，
   于是报「无法启用，端口被占用」，应用再也启不动；
3. pid 文件也没写，应用自己的 `status` 判断同样是错的。

所以 `hot-update` 现在是 `appcenter-cli stop` → 换文件 → `appcenter-cli start`，
状态、pid 文件、端口占用都由 fnOS 统一管，桌面里的显示一直对得上。

`appcenter-cli` 需要 root（要连 rpcbroker 的 unix socket），
应用目录下的文件普通用户也写不进去，所以整段脚本都用 sudo 跑。

### 热更新为什么连内核一起停

面板的 core 管理只认**自己启动的进程**（内存里的 `exec.Cmd`，没有 pid 文件、不认领已有内核）。
所以热更新如果只杀面板：

1. 面板被 `pkill` 掉，它的内核会被 init 收养变成**孤儿**，继续占着 7890/9090；
2. 新面板起来后又起一个内核，绑定 7890/9090 失败（日志里是 `address already in use`）；
3. 表面上「内核运行中」，实际在服务的是那个孤儿 —— 面板以为自己的内核活着，其实它是死的。

所以 `hot-update` 会把内核一起停掉，让新面板起一个干净的。
这跟 `round_dash.py` 里那个「清理孤儿内核」步骤是同一个道理。

### 两个安全上的改进

原来的 Python 脚本用 paramiko 的 `AutoAddPolicy`，**接受任何主机密钥** ——
中间人可以悄悄接管整条 SSH 通道，密码直接落到对方手里。
现在默认走 `known_hosts` 校验，对不上就拒绝连接（`hostkey` 子命令是显式的例外）。

另外 sudo 密码现在写进 stdin（`sudo -S`），**不出现在命令行里**，
`ps` 看不到 —— 原来的 `echo 密码 | sudo -S sh -c ...` 是明文躺在进程列表中的。

> 真机没法在开发机上验证时，`scripts/deploy` 自带一个进程内的 SSH 服务端做集成测试：
> `go test ./...` 会真跑一遍连接、执行、sudo 密码投递、SFTP 上传与 host key 不匹配拒绝。

## 在 fnOS 上调试

```bash
# 把包目录传上去，覆盖安装（比 fpk 反复重装方便得多）
appcenter-cli install-local -d /tmp/mihomo-src -v 1

# 启停
appcenter-cli start Mihomo
appcenter-cli stop Mihomo
appcenter-cli status Mihomo

# 看日志
tail -f /vol1/@appdata/Mihomo/mihomo-panel.log
tail -f /vol1/@appdata/Mihomo/mihomo.log
```

> `appcenter-cli install-fpk` 对**已安装的应用不做原地升级**（只提示 "is installed"），
> 同版本号也不会重新部署。改完代码请用 `install-local`，
> 或者在 Web UI 里手动卸载后重装。

## 测试

```bash
go test ./...     # 主模块，含 scripts/deploy
```

重点覆盖在 `internal/sub`（订阅解析的各种格式）、`internal/mihomo`（配置生成的
空密钥、重名节点去重）、`internal/api`（各接口的处理与接管失败自动撤销）、
`internal/app`（向导值套用、订阅合并规则）与 `internal/dashboard`（归档缓存与版本探测）。

`internal/core` 用测试进程自己冒充内核（`TestMain` 里按环境变量分流）：
真起真停，验的是「并发重启不报错」和「Stop 返回时进程真的退出了」。

`internal/safeio` 验的是原子写入：并发写不报错、落地是完整的一份、不留残留临时文件。

`internal/api` 里有一组鉴权用例：没设口令时接口放行、没登录被挡、错密码被挡、
登录后放行、改口令后旧会话作废、登出后旧会话作废、连续失败会锁定、
设置接口不回传口令哈希、明文口令不落盘。

`scripts/deploy`（NAS 部署工具）也在主模块里，`./...` 会连它一起测。
它起了一个进程内的 SSH 服务端当 NAS 的替身，真跑一遍连接、执行命令、
sudo 密码投递、SFTP 上传，以及 host key 不匹配时拒绝连接。

前端不在 Go 测试里，靠 `check_frontend` 守（见上）。

## 几个关键实现点

### 面板鉴权

面板监听在所有网卡上（`--port`，默认 8765），同网段谁都能访问。
在「基础设置 → 面板访问」里设了密码之后，除了登录接口以外的所有接口都要求先登录。

| 决定 | 理由 |
|---|---|
| 用会话 Cookie，不用 HTTP Basic Auth | 飞牛桌面是 iframe 嵌的，浏览器在 iframe 里弹 Basic Auth 认证框会被拦掉，页面只剩空白 |
| 口令存 PBKDF2-HMAC-SHA256 哈希 | 配置里不留明文。格式 `pbkdf2-sha256$迭代次数$盐$哈希`，每个口令配独立随机盐 |
| 会话令牌用 HMAC 签名，密钥进程内随机生成 | 面板重启后旧会话自然失效，不用考虑怎么擦干净密钥 |
| 令牌里带口令指纹 | 改了密码，旧会话立刻作废 |
| 令牌里带随机 nonce | 只靠秒级时间戳的话，同一秒里的两次登录会拿到完全相同的令牌 —— 登出一个就连带作废另一个 |
| 登出只作废自己这一份会话 | 「退出登录」不该把同一台 NAS 上别的设备一起踢下线 |
| 写接口只收 `application/json` | HTML 表单发不出 JSON，挡住最常见的跨站请求伪造 |
| 登录失败按来源 IP 计数，5 次锁 1 分钟 | 面板暴露在局域网，得防暴力猜密码 |
| 没设密码时不启用鉴权 | 内网自用不该被登录框挡住；老用户升级后行为不变 |

接口：

| 接口 | 鉴权 | 作用 |
|---|---|---|
| `GET /api/auth` | 否 | 报告是否启用了口令、当前请求是否已登录 |
| `POST /api/login` | 否 | 校验用户名口令，通过后下发 `mihomo_session` Cookie |
| `POST /api/logout` | 是 | 清 Cookie，并作废这一份会话（只作废自己这一份，不影响别的设备） |
| 其余 `/api/*` | 是 | 设了口令就要求带有效会话 |

代码位置：口令哈希在 `internal/settings/password.go`，会话与中间件在 `internal/api/auth.go`，
前端登录页在 `web/index.html` 与 `web/app.js`。
**改前端记得同步 `fnos/app/www/`** —— 那里是打包用的镜像。

旧版本要是把明文口令写进了 `config.json` 的 `panelPassword`，
读取时会自动迁成哈希并重新落盘，明文不会继续留在文件里。
派生失败时保留明文不落盘 —— 宁可让用户重设，也别悄悄退回免登录。

### 控制接口密钥

面板密码管的是 8765，管不到内核的控制口（默认 9090）。
控制口默认绑在所有网卡上，密钥为空的话同网段任何设备都能
`GET /9090/proxies` 读走全部节点凭据、`/configs` 直接改内核配置。

所以首次运行（配置文件还不存在时）会**自动生成一个随机控制密钥**
（`internal/app/secret.go`）。用户之后在「基础设置」里显式清空它是可以的，
清空后不会再被自动塞回去 —— `settings.Store.IsFresh()` 记着是不是首次运行。
第三方面板的地址本来就带着密钥（`internal/dashboard/dashboard.go` 拼 URL 时加上），
自动生成不会影响「打开控制面板」。

### 第三方面板归档

第三方面板（MetaCubeXD / zashboard / Yacd）和内核、geodata 一样是
「随包分发 + 本地留一份」的资源，规则也一样：

- **打包前**：`go run ./scripts fetch_dashboard` 把归档下到 `assets/`，
  打包时由 `copy_dashboard` 拷进包的 `app/bin/dashboards/`。
  没跑过这一步也能打包，只是包里没有面板归档，运行时按需下载。
- **运行时**：面板优先用本地那份 —— 随包分发的，
  或之前缓存到 `<数据目录>/cache/dashboards/` 的。
  只有本地没有、或上游出了新版本，才去 GitHub 重新拉。
- **版本怎么判**：
  - MetaCubeXD / zashboard 看 GitHub release 的 tag
    （`releases/latest` 会 302 到带 tag 的地址，读重定向头即可）；
  - Yacd 用的是 gh-pages 分支快照，取分支最新提交的 SHA 当版本号。

**为什么不用 `api.github.com` 查版本**：那边按出口 IP 限流，
同一台 NAS 上几个面板一起探测很容易撞上 403。
重定向头和 atom 订阅都不用认证，也没有这个限制。

**版本记录在归档旁边的 `dashboards.json` 里**（版本号、ETag、大小、SHA256、
下载与探测时间）。清单和归档必须放在同一个目录 ——
换目录就等于换了一套本地副本，版本号得跟着归档走。

### 控制面板地址与 IPv6

「控制面板」页给第三方面板的地址，是用**访问者实际用的那个 Host** 拼出来的，
不能写死 127.0.0.1 —— 否则用户在自己电脑上打开面板，
面板会去连访问者电脑上的 9090，必然连不上。

IPv6 有个坑：**hostname 参数必须带方括号**（`[2409::982]`）。
三个面板拿到 hostname 参数后都是自己拼地址的，拼法不一样，坏法也不一样：

| 面板 | 拼法 | 裸 IPv6 的后果 |
|---|---|---|
| MetaCubeXD | `${protocol}//${hostname}:${port}` | 拼出 `http://2409::982:9090`，浏览器解析不了 |
| zashboard | `${protocol}://${host}:${port}` | 同上 |
| yacd | `url.hostname = hostname` | URL 的主机名解析器遇到冒号判为非法，赋值被忽略，面板退回默认地址 |

第三个看着最安全（走 WHATWG URL），实际一样连不上：
实测 Node 的 URL 实现，把裸 IPv6 赋给 `url.hostname` 会被**直接忽略**，
只有自带方括号才生效。所以 `internal/dashboard` 里的 `bracketIPv6`
给 IPv6 统一补括号，三个面板共用这一份实现，不给它们各加开关。

**内核侧不用动**。`external-controller` 写 `0.0.0.0:9090` 时，
mihomo 实际监听的是双栈的 `*:9090`（IPv4、IPv6 都连得上，NAS 上实测过），
所以配置里不用改。倒是别改成 `[::]:9090`：那个值会被 YAML 当成流序列，
启动时直接报 `Parse config error: yaml: line 1: did not find expected node content`，
端口根本起不来。
### 订阅格式

机场按 User-Agent 返回不同内容：

- `User-Agent: clash.meta` → 完整 Clash YAML（含 `proxies`、`proxy-groups`、`rules`）
- 其它 UA → base64 编码的链接列表

本应用用 `clash.meta` 请求。拿到 Clash YAML 时，**节点、策略组、分流规则全部采用
机场自己的** —— 机场的分流策略通常比自己重建更贴合用户意图。只有订阅没给策略组时，
才由本应用生成「节点选择 / 自动选择 / 故障转移」三个基础组。

### 重名节点

多个订阅合并时可能出现同名节点，mihomo 遇到重名会直接拒绝启动。
所以在生成配置时要全局去重（加数字后缀），并同步修正策略组里对这些名字的引用。

### 空密钥

`secret` 字段为空时不能写成 `secret: ""` —— yaml.v3 会输出 `secret: ''''''`，
mihomo 会把六个单引号当成真正的密钥，认证就废了。
所以用 `*string`，空值时为 nil，让 YAML 完全不输出这个字段。

### DNS

mihomo 的 DNS 配置里 `proxy-server-nameserver` 是必需的：
少了它，解析机场域名时会走 fake-ip 的正常流程而拿到假 IP，
表现为 `dns resolve failed: context deadline exceeded`。

`default-nameserver` 必须是纯 IP，否则会陷入「解析 DNS 服务器 → 又要 DNS」的循环。

### 接管 NAS 本机出站流量（TUN 模式）

让 NAS 自己的流量走代理，走的是 mihomo 的 **TUN 模式**：它自己创建虚拟网卡
`Mihomo0`（198.18.0.1/30）并接管路由，本机进程发出的包全部经过它。

**IPv6 默认接管**，配置里有两处，缺一不可：

- 顶层 `ipv6: true` —— mihomo 自身的默认值就是 true，之前本项目写死成 false，
  等于主动关掉了内核的 IPv6 能力；
- `tun.inet6-address: [fdfe:dcba:9876::1/126]` —— **不配这一项，
  Linux 的 auto-route 就不会把 IPv6 流量引进虚拟网卡**，IPv6 会绕过代理直连。
  症状是 ping 能拿到目标真实的 IPv6 地址、而走代理的 IPv6 站点始终不通。

**IPv6 接管后实测要注意两件事**（在真 NAS 上验证过）：

- `fake-ip` 模式**不签发 AAAA 记录**，所以 `curl -6`、纯 IPv6 的站点仍然连不上
  （客户端会回落 IPv4，IPv4 是走代理的，不影响使用）；
- 外网 IPv6 要**节点自己也有 IPv6** 才能通。节点只有 IPv4 时，
  IPv6 目标会超时 —— 这时候 IPv4 流量是正常的。

接管本身是生效的：IPv6 的 DNS 查询会进 TUN 并拿到 fake-ip，
不会再像以前那样把目标的真实 IPv6 地址直接泄露出去。

**刻意不开 `strict-route`**：那个选项会让所有流量（包括局域网）都必须经过 TUN，
内核一挂 NAS 就连后台都进不去了。现在这种「默认路由劫持 + 局域网走更具体路由」
的方式，内核挂了局域网访问不受影响。

**为什么不用 nftables 的 output 钩子？** 两个原因，都是实测踩到的：

1. 内核的 `tproxy` 表达式只支持 prerouting（入站）钩子。在 output 钩子加规则会直接报
   `Error: Could not process rule: Operation not supported`。
2. `redirect` 虽然在 output 链可用，但会丢掉原始目的地址，mihomo 拿不到真实目标。

所以 TUN 是接管本机出站流量的标准做法。

**安全网**：开启接管后必须验证 TUN 设备是否真的创建出来。验证不通过就自动撤销
（把 `gateway.enabled` 改回 false、重新生成配置、重启内核），
不能让 NAS 处在「接管了一半、上不了网」的状态。TUN 会改整机路由，这一步是必须的。

**为什么不做旁路由（接管局域网客户端）？**

试过，代价太大：客户端必须同步把网关改成 NAS，否则开启后整个局域网立刻断网
（当时实测 NAS 自己的 SSH 和 Web 都连不上，只能从 IPv6 绕进去才救回来）。
而且客户端网关一旦没配好，排查成本很高。用户明确要求移除这个功能。

现在只接管本机流量，客户端完全不受影响 —— 这是更安全也更实用的取舍。

### 配置存储的内存语义

`settings.Store` 在**启动时读一次文件，之后只读内存**。所以：

- 外部直接改 `config.json` 文件不会生效，进程内存里的值还是旧的。
- 要改设置必须走面板的 `/api/settings` 或 `/api/gateway/toggle` 接口。

调试时这一点很容易踩：我一度以为是代码 bug，实际上是改错了地方。

### 端口一致性

面板端口 8765 写死在三处，改端口必须三处同步：

- `fnos/manifest` 的 `service_port`
- `fnos/app/ui/config` 的 `port`
- `fnos/cmd/main` 里的 `--port`

飞牛的桌面入口是静态配置，不像某些平台支持 `${wizard_port}` 变量替换。
