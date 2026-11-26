# rchrome：一条命令让本机的 Claude 操控远程主机的 Chrome

> 本文是给**实现者**（AI 编码助手或开发者）的完整规格。读完即可从零建仓库、实现、测试、发版。
> 最终用户是开发者，但对他们的要求必须是：两端各敲一条命令，不注册账号、不配路由器、不改防火墙、不需要 root。
>
> **实现状态**：v0.1.0 已按本文实现（Go，嵌入 tailcat 库），macOS 实测全链路通过。与本文的已知偏差：发版用 `scripts/build-release.sh` + GitHub Actions 而非 GoReleaser（产物命名与 §7.1 一致）；隧道对外固定暴露 9222，Chrome 实际端口被占用时自动换（§4.1）；多台远程主机已在 v0.1 支持（`--name` / `--label` / `rchrome list`）。

---

## 0. 一句话

远程主机跑 `rchrome serve`，本机跑 `rchrome connect <地址>`，之后本机 Claude Code 里的浏览器工具操作的就是远程主机的 Chrome。

---

## 1. 背景与原理

### 1.1 为什么要做这个

- Claude Code 自带的 Claude in Chrome 扩展可以跨机器，但它按 **Claude 账号** 路由：另一台机器必须装扩展并登录同一账号，选择器只显示 Browser 1 / Browser 2，还会静默切到别的机器。用户不接受「在别人的机器上登录自己的账号」。
- 云浏览器（Browserbase 等）要账号、要付费，且不是用户自己的那台机器。
- 手工方案（Chrome 开调试端口 + SSH 隧道 + 改 MCP 配置）步骤太多，而且两台机器往往不在同一局域网、没有公网 IP。

### 1.2 原理（两层）

```
┌──────────── 远程主机 ────────────┐          ┌──────────── 本机 ────────────┐
│ Chrome                           │          │ Claude Code                  │
│  --remote-debugging-port=9222    │          │   └─ MCP 子进程: rchrome mcp │
│  (只监听 127.0.0.1)              │          │        ├─ 本地监听 127.0.0.1:9222
│        ▲                         │          │        │     │ (隧道客户端)  │
│        │ 127.0.0.1:9222          │          │        │     ▼              │
│  rchrome serve                   │  加密隧道 │        │  tailcat Client ───┼──┐
│   └─ tailcat Server ─────────────┼──────────┼────────┘                    │  │
│      (WireGuard + NAT穿透 + DERP)│          │        └─ 子进程: npx chrome-devtools-mcp
└──────────────────────────────────┘          │             --browserUrl http://127.0.0.1:9222
                                              └──────────────────────────────┘
```

1. **协议层**：Chrome 自带 CDP（Chrome DevTools Protocol）调试端口。Google 官方的 `chrome-devtools-mcp` 和微软的 `@playwright/mcp` 都能连一个已存在的 CDP 端点。我们不发明浏览器协议，只负责把远程的 9222 搬到本机的 127.0.0.1:9222。
2. **隧道层**：用 **tailcat**（Tailscale 2026 年开源的 Go 库 + CLI，BSD-3 协议）。它只取了 Tailscale 的数据面：WireGuard 加密、NAT 穿透、打洞失败时走公共 DERP 中继。**没有账号、没有控制面、不建 TUN 网卡、不需要 root**。连接凭证是一个 `tc` 开头的地址串，里面编码了服务端公钥、预共享密钥和中继信息。

把 tailcat **作为 Go 库嵌入**，rchrome 就是一个无依赖的单文件二进制。

---

## 2. 目标与非目标

### 目标（v1 必须全部满足）

| # | 要求 |
|---|---|
| G1 | 远程主机：`rchrome serve` 一条命令 → 自动找到并启动 Chrome（调试模式、独立 profile）→ 打出一个地址串和「本机该敲的命令」 |
| G2 | 本机：`rchrome connect <地址>` 一条命令 → 验证连通 → 自动把 MCP 注册进 Claude Code → 打印「可以用了」 |
| G3 | 之后用户完全不再碰 rchrome：每次新开 Claude Code，MCP 子进程自动拉隧道、自动起 chrome-devtools-mcp，会话结束自动断 |
| G4 | 远程主机重启 `serve`，地址不变（持久密钥），本机不用重新 connect |
| G5 | 支持远程主机 Windows / macOS / Linux，本机 macOS / Linux / Windows |
| G6 | 两台机器可以在不同网络（例如家里 Wi-Fi 和手机热点），无公网 IP、无端口映射 |
| G7 | 无账号、无注册、无 root、无外部运行时依赖（除本机需要 Node 20+ 跑 chrome-devtools-mcp） |
| G8 | **一条命令安装**：用户不 clone 仓库、不装 Git / Go、不编译。安装脚本从 GitHub Releases 下载对应平台的单二进制，校验后放进 PATH；脚本可直接接着执行 `serve`，远程主机从零到打出地址只敲一行 |
| G9 | Claude **自动识别**：首次 `connect` 之后，每次新开 Claude Code 会话，远程浏览器工具自动出现在工具列表，并在握手时注入说明让 Claude 知道这些工具指向哪台机器；用户不需要先给 Claude 任何提示 |
| G10 | 远程主机是只有 Safari 的 Mac 时，`rchrome serve --safari` 走 Apple 官方 `safaridriver --mcp`，本机命令与体验不变 |

### 非目标（v1 不做，留到 §10）

同时连多台远程机、局域网自动发现（v1.1）、Web 管理界面、Windows 服务 / systemd 常驻、二维码、GUI。真 Safari 之外的 WebKit 内核路线见附录 C。

---

## 3. 用户体验（输出文案必须与此一致）

### 3.0 安装（两台机器都是这一行，不需要 Git / Go / 编译）

macOS / Linux：

```bash
curl -fsSL https://raw.githubusercontent.com/ceeyang/rchrome/main/scripts/install.sh | sh
```

Windows（PowerShell）：

```powershell
irm https://raw.githubusercontent.com/ceeyang/rchrome/main/scripts/install.ps1 | iex
```

远程主机想一行到底，安装完直接开始服务：

```bash
curl -fsSL https://raw.githubusercontent.com/ceeyang/rchrome/main/scripts/install.sh | sh -s -- serve
```
```powershell
$env:RCHROME_RUN='serve'; irm https://raw.githubusercontent.com/ceeyang/rchrome/main/scripts/install.ps1 | iex
```

脚本输出：

```
rchrome v1.0.0  darwin/arm64  → /usr/local/bin/rchrome   ✓ 校验通过
```

可选渠道：`brew install ceeyang/tap/rchrome`、`scoop install rchrome`（见 §7）。本机侧另需 Node.js 20+（chrome-devtools-mcp / playwright-mcp 通过 `npx` 运行），`connect` 会检查并给出安装提示；远程主机不需要 Node。

### 3.1 远程主机

```
$ rchrome serve
🐈 Chrome 已启动   调试端口 9222   profile ~/.rchrome/profile
🐈 地址（持久，重启不变）
   tcomFwWCCcjS5nKNqAod034nWoJZW0LZqDhhC8U_dKdnDRYQ8uNGFpGQEu

在本机运行：
   rchrome connect tcomFwWCCcjS5nKNqAod034nWoJZW0LZqDhhC8U_dKdnDRYQ8uNGFpGQEu

Ctrl+C 结束（会一起关闭 Chrome）
```

连接进来时追加一行：`→ 本机已连接  nodekey:cfb6…dfd16  直连 / 经中继 nyc`。

远程主机只有 Safari（macOS）时：

```
$ rchrome serve --safari
🐈 safaridriver 已就绪   Safari 27.2   --mcp 可用
   请确认 Safari → 开发 → 已勾选「允许远程自动化和外部代理」
🐈 地址（持久，重启不变）
   tcomFwWCCcjS5nKNqAod034nWoJZW0LZqDhhC8U_dKdnDRYQ8uNGFpGQEu

在本机运行：
   rchrome connect tcomFwWCCcjS5nKNqAod034nWoJZW0LZqDhhC8U_dKdnDRYQ8uNGFpGQEu --backend safari
```

### 3.2 本机

```
$ rchrome connect tcomFwWCCcjS5nKNqAod034nWoJZW0LZqDhhC8U_dKdnDRYQ8uNGFpGQEu
✓ 已连上远程 Chrome   Chrome/142.0.7444.59   Windows
✓ 已注册到 Claude Code   名称 remote-chrome   后端 chrome-devtools-mcp
新开一个 Claude Code 会话，对它说「打开 example.com 并截图」试试。
```

### 3.3 之后

用户只需保证远程主机的 `rchrome serve` 还在跑。本机每次新开 Claude Code 会话，Claude Code 启动 MCP 子进程 `rchrome mcp`，它自己拉隧道、起 chrome-devtools-mcp；会话结束，子进程退出，隧道随之关闭。

**Claude 不需要任何额外提示。** 它的工具列表里会出现 `remote-chrome__navigate_page`、`remote-chrome__take_screenshot` 等工具，握手说明（§5.5.1）里写着「这组工具操作的是远程主机 Windows 上的 Chrome/142，不是本机浏览器」。用户说「打开 example.com 截图」，Claude 直接用。

远程 `serve` 没在跑时，工具仍然在列表里；Claude 调用时收到一条明确的中文错误（§5.9），会原话转给用户。

---

## 4. 命令规格

所有子命令：`--help`、`--verbose`（隧道日志到 stderr）、`--json`（机器可读输出，给其他 AI 调用用）。

### 4.1 `rchrome serve`

| flag | 默认 | 说明 |
|---|---|---|
| `--port` | 9222 | Chrome 调试端口。被占用（常见：用户日常的 Chrome 已开了 9222）时自动换一个空闲端口。**隧道对外固定暴露 9222**，与 Chrome 实际端口无关，所以本机 `connect` 永远不用指定端口 |
| `--profile` | `~/.rchrome/profile` | Chrome user-data-dir。**必须**是非默认目录，Chrome 136+ 拒绝在默认 profile 上开调试端口 |
| `--chrome <path>` | 自动查找 | 指定浏览器可执行文件 |
| `--no-launch` | false | 不启动 Chrome，只把已经在跑的 `127.0.0.1:<port>` 搬出去 |
| `--keep-chrome` | false | Ctrl+C 时不关 Chrome |
| `--allow <nodekey>` | 空 | 只允许这些客户端公钥连接，可多次 |
| `--headless` | false | 以 `--headless=new` 启动 Chrome |
| `--key new` | 使用持久密钥 | 本次用一次性密钥（地址一次性） |
| `--safari` | false | macOS 专用。不启动 Chrome；每个隧道连接拉起一个 `/usr/bin/safaridriver --mcp` 进程并桥接其 stdio。与 `--port` / `--profile` / `--chrome` / `--headless` 互斥 |

行为：
1. 查找 Chrome（§5.2）。找不到 → 退出码 2，提示 `--chrome`。
2. 若 `--no-launch` 为假：启动 Chrome，轮询 `http://127.0.0.1:<port>/json/version` 最多 20 秒，拿到 `Browser` 字段。
3. 加载或创建持久密钥（§5.3），启动 tailcat Server，仅接受发往 `<port>` 的连接，其余端口直接关闭。
4. 打印 §3.1 文案。`--json` 时输出 `{"addr":"tc…","port":9222,"browser":"Chrome/142…","os":"windows"}`。
5. 阻塞直到 SIGINT / SIGTERM：关闭 Server，按 `--keep-chrome` 决定是否结束 Chrome（Windows 上要结束整棵进程树）。
6. `--safari` 时，步骤 1、2 换成：检查 `/usr/bin/safaridriver --help` 输出含 `--mcp`（Safari 27+），否则退出码 6 并提示升级 Safari。隧道上暴露约定端口 **4445**（常量 `SafariMCPPort`）。OnTCP 收到连接 → 启动 `/usr/bin/safaridriver --mcp` → 连接与进程 stdin/stdout 双向拷贝 → 连接断开即结束进程。同一时刻只允许一个连接（safaridriver 只支持一个会话），第二个连接收到一行 JSON-RPC error 后关闭。「允许远程自动化和外部代理」是 Safari 的用户设置，rchrome 只能提示，不能代为勾选。

### 4.2 `rchrome connect <addr>`

| flag | 默认 | 说明 |
|---|---|---|
| `--backend` | `devtools` | `devtools` = chrome-devtools-mcp；`playwright` = @playwright/mcp（也是连 CDP，**仅 Chromium 系**）；`both` = 两个都注册；`safari` = 对应远程的 `serve --safari`。Chrome MCP 与 Playwright 都可以，默认 devtools。附录 C 的 `playwright-server` 为隐藏选项 |
| `--name` | `remote-chrome` | 注册进 Claude Code 的 MCP 名称，也是工具前缀（`win-office__take_screenshot`）。多台远程主机靠它区分；`--backend safari` 时默认 `remote-safari` |
| `--label` | 同 `--name` | 备注名，注入握手说明给 Claude 看（如「办公室的 Windows」） |
| `--remote-port` | 9222 / 4445 | 一般不用动；附录 C 的 playwright-server 路线用它指到 3000 |
| `--scope` | `user` | 传给 `claude mcp add -s` |
| `--no-register` | false | 不动 Claude Code，只打印可粘贴的 JSON 配置（给 Cursor、Codex 等其他客户端） |
| `--local-port` | 9222 | 本机监听端口；被占用时自动换空闲端口并提示 |

行为：
1. 解析地址，起 tailcat Client，`DialTCPPort(remotePort)`，通过隧道 `GET /json/version`（Host 头写 `127.0.0.1:<port>`），解析 `Browser` 和 `User-Agent` 得到版本、系统。失败 → 退出码 3，提示「远程主机是否在跑 rchrome serve」。`--backend safari` 时改为拨 4445，发送一条 MCP `initialize` 请求，读到带 `serverInfo` 的响应即算连通，随即关闭连接释放 safaridriver。
2. 写 `~/.rchrome/connections/<name>.json`（并复制一份到 `connect.json` 作「最近一次」）：`{"name":"win-office","label":"办公室的 Windows","addr":"tc…","remote_port":9222,"local_port":9222,"backend":"devtools","os":"Windows","browser":"Chrome/142.0.7444.59"}`。`label` / `os` / `browser` 供 §5.5.1 注入说明用。
3. 注册到 Claude Code（§5.6）。注册的命令把所有参数显式带上（`mcp --backend … --name … --label … --desc "Windows, Chrome/142" --local-port … --remote-port … <addr>`），这样多台远程主机互不依赖文件状态。
4. 打印 §3.2 文案，并打印本机身份 `nodekey:…`（给远程 `serve --allow` 用）。

### 4.3 `rchrome mcp [addr]`

Claude Code 实际调用的命令，用户一般不直接敲。

1. `addr` 省略时读 `~/.rchrome/connect.json`。
2. **懒连接**：先起本机监听 `127.0.0.1:<local_port>`（被占用就换空闲端口），立刻启动子进程（§5.5），不等远端。隧道在第一个 accept 到的连接时才 `client.DialTCPPort(ctx, remotePort)` → 双向 `io.Copy`。这样远程 `serve` 没开时工具照样注册成功，错误推迟到调用时刻（§5.9）。
3. **stdin / stdout 原样透传给子进程**，rchrome 自己的任何输出只能写 stderr 或日志文件，否则会污染 MCP 的 JSON-RPC 流。唯一例外是 §5.5.1 的握手改写。
4. `--backend safari`：没有子进程。拨远端 4445，把自己的 stdin/stdout 与隧道连接双向拷贝；同样做 §5.5.1 的握手改写。
5. 子进程退出（或 Safari 隧道连接断开）→ 关监听、关 Client → 以子进程的退出码退出。收到 SIGINT / SIGTERM → 转发给子进程，等待最多 5 秒后强杀。

### 4.3.1 `rchrome list`

列出 `~/.rchrome/connections/*.json`：名称（工具前缀）、备注、后端、系统、浏览器、地址前 16 位。`--json` 机器可读。

### 4.3.2 `rchrome keygen`

生成（或读取）`~/.rchrome/client.key.json`，打印本机 `nodekey:…`。`connect` / `mcp` / `forward` 自动使用这把身份，所以远程 `serve --allow <nodekey>` 后只有这台机器能连。

### 4.4 `rchrome forward <addr>`

纯端口转发，前台运行，给不用 Claude Code 的人（如直接用 Playwright 脚本 `connectOverCDP('http://127.0.0.1:9222')`）。flag：`--local-port`、`--bind`（默认 127.0.0.1）。

### 4.5 `rchrome doctor`

逐项检查并打 ✓ / ✗：Chrome 可执行文件；`node --version` ≥ 20、`npx` 存在；`claude` CLI 存在；`~/.rchrome` 可写；若有 `connect.json` 则 Ping 远端并报告直连还是中继。

### 4.6 `rchrome version`

打印版本、提交号、嵌入的 tailcat 版本。

---

## 5. 实现细节

### 5.1 语言与依赖

- Go 1.23+，module 路径 `github.com/ceeyang/rchrome`。
- 直接依赖：`github.com/tailscale/tailcat`（隧道）。子命令分发用标准库 `flag` + 手写 switch，不引 cobra，减少体积和依赖。
- 不在 Go 侧依赖 Node。`chrome-devtools-mcp` / `@playwright/mcp` 用本机的 `npx` 按需拉取。

### 5.2 Chrome 查找与启动

查找顺序（取第一个存在的文件）：

| 系统 | 候选 |
|---|---|
| Windows | `%ProgramFiles%\Google\Chrome\Application\chrome.exe`、`%ProgramFiles(x86)%\Google\Chrome\Application\chrome.exe`、`%LocalAppData%\Google\Chrome\Application\chrome.exe`、注册表 `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\chrome.exe`；兜底 Edge `msedge.exe`（同目录规则，`Microsoft\Edge`） |
| macOS | `/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`、`~/Applications/…`、兜底 `Chromium.app`、`Microsoft Edge.app` |
| Linux | PATH 里的 `google-chrome`、`google-chrome-stable`、`chromium`、`chromium-browser`、`microsoft-edge` |

启动参数：

```
--remote-debugging-port=<port>
--user-data-dir=<profile>
--no-first-run
--no-default-browser-check
[--headless=new]
about:blank
```

就绪判定：`GET http://127.0.0.1:<port>/json/version` 返回 200 且 JSON 含 `webSocketDebuggerUrl`，每 250ms 轮询一次，最多 20 秒。

进程管理：记录 PID；Windows 用 `taskkill /T /F /PID` 结束进程树，类 Unix 用进程组（`Setpgid`）后 `SIGTERM`，3 秒后 `SIGKILL`。

### 5.3 隧道：服务端

```go
import "github.com/tailscale/tailcat"

s := &tailcat.Server{
    OnTCP: func(port uint16) func(net.Conn) {
        if port != cfg.Port { return nil }        // 其它端口拒绝
        return func(c net.Conn) {
            defer c.Close()
            up, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
            if err != nil { return }
            defer up.Close()
            pipe(c, up)                           // 双向 io.Copy，任一方向结束即关闭
        }
    },
}
// 持久密钥、固定 DERP region、AllowClient 见下
if err := s.Start(); err != nil { ... }
fmt.Fprintln(os.Stderr, s.TailcatAddr())
```

**持久密钥（G4）**：地址由服务端私钥决定。首次运行用 `tailcat.NewPrivateKey()` 生成，序列化到 `~/.rchrome/server.key.json`（权限 0600），之后每次加载。把它交给 `Server` 的私钥字段。同时固定 DERP region（否则 `auto` 每次启动可能换区，地址随之变化）。**字段名和 region 的设置方式以当时 pkg.go.dev 为准，直接参照 tailcat 仓库 `cmd/tailcat` 里 `genkey --key=… --fixed-region` 与 `serve --key=…` 的实现照搬**，不要自己猜。

**`--allow`**：把公钥放进 `tailcat.KeySet`，赋给 `Server.AllowClient`。

### 5.4 隧道：客户端

```go
cl := tailcat.NewClient(tailcat.Addr(addr))
defer cl.Close()
ln, _ := net.Listen("tcp", "127.0.0.1:"+localPort)
for {
    c, err := ln.Accept(); if err != nil { break }
    go func() {
        defer c.Close()
        up, err := cl.DialTCPPort(ctx, remotePort)
        if err != nil { return }
        defer up.Close()
        pipe(c, up)
    }()
}
```

客户端身份：默认每次随机。若用户要配合服务端 `--allow`，提供 `rchrome keygen` 生成并保存 `~/.rchrome/client.key.json`，`connect` / `mcp` 自动加载到 `Client.Key`。（v1 可选，v1.1 必做。）

**本地端口与 Chrome 的 Host 校验**：Chrome 只接受 Host 为 `localhost` / IP 字面量的请求，隧道落在 `127.0.0.1` 所以天然通过。Chrome 用请求的 Host 头拼 `webSocketDebuggerUrl`，所以本地端口和远程端口不同也能工作；实现后务必用 `curl -s http://127.0.0.1:<local>/json/version` 验证返回的 ws 地址端口正确。

### 5.5 MCP 子进程托管

| backend | 命令 |
|---|---|
| devtools | `npx -y chrome-devtools-mcp@<ver> --browserUrl http://127.0.0.1:<local_port>` |
| playwright | `npx -y @playwright/mcp@<ver> --cdp-endpoint=http://127.0.0.1:<local_port>` |

- `<ver>` 默认 `latest`，可用 `--mcp-version` 固定。
- Windows 上可执行文件是 `npx.cmd`。
- `cmd.Stdin = os.Stdin`、`cmd.Stdout = os.Stdout`、`cmd.Stderr` 指向 `~/.rchrome/logs/mcp-<date>.log`（也 tee 一份到 rchrome 的 stderr 便于 Claude Code 的 `/mcp` 面板看错误）。
- 等隧道监听 ready 后再启动子进程，避免子进程第一次连接就失败。
- 找不到 `npx` → 退出码 4，stderr 打印「需要 Node.js 20+，安装后重试」。
- `--backend playwright` 同样是连 CDP 端点，所以**只对 Chrome / Edge / Chromium 有效**。Playwright 连不上真 Safari（它的 webkit 是自带内核），真 Safari 走 `safari` 后端；WebKit 内核路线见附录 C。
- `--backend safari`：无子进程，纯 stdio 管道，见 §4.3 第 4 条。

### 5.5.1 握手改写：让 Claude 知道这是远程浏览器

MCP stdio 传输是按行分隔的 JSON-RPC。`rchrome mcp` 在透传时只做一件事：

1. 读 stdin 流，记下 `method == "initialize"` 那条请求的 `id`。
2. 读子进程（或 Safari 隧道）的 stdout 流，找到 `id` 相同的响应，把 `result.instructions` 设为下面这段（已有内容则追加在前面）：

   > 这组工具操作的是远程主机（<os>，<browser>）上的浏览器，经 rchrome 隧道转发，不是本机浏览器。用户要求操作「远程」「那台电脑」「Windows 上的 Chrome」时用这组工具。

   实现时的实际文案（含备注名）：「这组工具（前缀 <name>）操作的是远程主机「<label>」（<os>, <browser>）上的浏览器，经 rchrome 隧道转发，不是本机浏览器。用户提到「<label>」、「远程」、「那台电脑」或远程主机上的浏览器时用这组工具；用户要操作本机浏览器时不要用。」后接一段英文。`<label>` / `<os>` / `<browser>` 由 `connect` 探测后通过 `mcp` 的命令行参数传入。
3. 其余每一行一个字节不改；解析失败的行原样透传。

这是「Claude 自动识别」的关键。Claude Code 会把 instructions 原样展示给模型，模型因此知道工具指向哪台机器，而不只是看到又一组浏览器工具。配合默认名 `remote-chrome`，即使本机同时连着 Claude in Chrome 扩展和本地 Playwright MCP，模型也能选对。

### 5.6 Claude Code 注册

```
claude mcp remove -s <scope> <name>          # 忽略失败
claude mcp add    -s <scope> <name> -- <rchrome绝对路径> mcp <addr> --backend devtools --local-port <n>
```

- `<rchrome绝对路径>` 用 `os.Executable()` 取，避免 PATH 变化导致 Claude Code 找不到。
- `--backend both` 时注册两个名字：`<name>` 和 `<name>-pw`。
- `claude` 不在 PATH 或 `--no-register`：打印下面这段，让用户自己粘：

```json
{
  "mcpServers": {
    "remote-chrome": {
      "command": "/abs/path/rchrome",
      "args": ["mcp", "tc…", "--backend", "devtools"]
    }
  }
}
```

### 5.7 配置目录 `~/.rchrome/`

```
server.key.json   服务端持久私钥（0600），首次 serve 生成并把中继 RegionID 写回，地址从此固定
client.key.json   客户端持久身份（首次 connect 自动生成）
connections/      每台远程主机一个 <name>.json
connect.json      本机最近一次 connect 的参数
profile/          Chrome user-data-dir（远程主机侧）
logs/             mcp-*.log、serve-*.log
```

Windows 下 `~` 取 `%USERPROFILE%`。

### 5.8 退出与清理

- `serve`：SIGINT / SIGTERM → 关 Server → 结束 Chrome（除 `--keep-chrome`）→ 退出 0。
- `mcp`：子进程退出 → 关监听 → 关 Client → 以子进程退出码退出；不留下任何监听端口。
- 所有 goroutine 用 `context` 控制，不得泄漏。

### 5.9 错误信息

面向用户的错误一律中英双语一行，先中文。示例：

```
✗ 连不上远程主机：请确认那边 rchrome serve 还在运行，地址是否复制完整 / Cannot reach remote: is `rchrome serve` running?
```

退出码：0 成功；2 找不到 Chrome；3 隧道连接失败；4 缺 Node / npx；5 Claude CLI 注册失败；6 safaridriver 缺失或不支持 `--mcp`；1 其它。

**工具调用时刻的错误**（懒连接下远程未运行）：`rchrome mcp` 拨号失败时不要让子进程裸报 ECONNREFUSED。在本地监听处直接回一个 HTTP 503，body 为：

```
远程主机的 rchrome serve 未运行，或地址已失效。请在远程主机执行 rchrome serve 后重试。 / Remote rchrome serve is not running.
```

chrome-devtools-mcp / playwright-mcp 会把这段文字带进工具错误，Claude 原话转给用户。Safari 后端则向 stdout 写一条 JSON-RPC error（code -32000，同一段文字）。

---

## 6. 仓库结构

```
rchrome/
├── cmd/rchrome/main.go          子命令分发、全局 flag
├── internal/
│   ├── chrome/                   find_*.go（按 GOOS 拆文件）、launch.go、ready.go
│   ├── tunnel/                   server.go、client.go、keys.go、pipe.go
│   ├── mcp/                      child.go（子进程托管）、register.go（claude mcp add）
│   ├── config/                   dir.go、connect.go
│   └── ui/                       文案常量、--json 输出
├── scripts/
│   ├── install.sh                curl -fsSL …/install.sh | sh
│   └── install.ps1               irm …/install.ps1 | iex
├── .goreleaser.yaml
├── .github/workflows/release.yml
├── README.md                     中文为主，顶部一段英文 TL;DR
├── SPEC.md                       本文
└── LICENSE                       MIT
```

---

## 7. 发布

### 7.1 产物

- `scripts/build-release.sh <tag>`（纯 sh + go build，CI 与本地共用；GoReleaser 可作替代），`CGO_ENABLED=0`，目标：`darwin/arm64`、`darwin/amd64`、`windows/amd64`、`windows/arm64`、`linux/amd64`、`linux/arm64`。
- `-ldflags "-s -w -X main.version=… -X main.commit=…"`，`version` 子命令读取。
- **文件名不带版本号**，这样 `releases/latest/download/` 这条固定 URL 永远指向最新版：
  - `rchrome_darwin_arm64.tar.gz`、`rchrome_darwin_amd64.tar.gz`、`rchrome_linux_amd64.tar.gz`、`rchrome_linux_arm64.tar.gz`
  - `rchrome_windows_amd64.zip`、`rchrome_windows_arm64.zip`
  - `checksums.txt`（sha256）
- 下载 URL：`https://github.com/ceeyang/rchrome/releases/latest/download/<文件名>`；固定版本用 `releases/download/v1.0.0/<文件名>`。

### 7.2 CI

`.github/workflows/release.yml`：push tag `v*`（或手动 dispatch 指定 tag）→ `scripts/build-release.sh` → `gh release create`（不存在时）+ `gh release upload --clobber`。`.github/workflows/ci.yml`：`main` 每次 push 跑 `go vet` + `go test ./...` + 一次全平台交叉编译，保证随时可发。

### 7.3 `scripts/install.sh`（POSIX sh，不依赖 bash）

1. 探测 `uname -s` / `uname -m` → `darwin|linux` × `arm64|amd64`；不支持的组合给出明确错误。
2. 版本：默认 `latest`；环境变量 `RCHROME_VERSION=v1.0.0` 可固定。
3. 用 `curl -fsSL`（没有 curl 则 `wget -qO-`）下载 tar.gz 和 `checksums.txt` 到临时目录，`sha256sum` 或 `shasum -a 256` 校验，不匹配即中止。
4. 解压得到 `rchrome`，`chmod 755`，先写 `rchrome.new` 再 `mv` 覆盖（正在运行的旧进程不受影响）。安装目录：`RCHROME_INSTALL_DIR` → `/usr/local/bin`（可写）→ `/opt/homebrew/bin`（可写，Apple Silicon 常见）→ 否则 `~/.local/bin`，并在它不在 PATH 时打印需要追加到 shell rc 的那一行。管道执行时无法 sudo 提示，所以不尝试 sudo。
5. macOS 对下载的二进制执行 `xattr -d com.apple.quarantine`，避免 Gatekeeper 拦截。
6. 打印 `rchrome <ver>  <os>/<arch>  → <path>   ✓ 校验通过`。
7. **透传参数**：`sh -s -- serve [flags]` 时，安装完成后 `exec rchrome serve [flags]`，让远程主机一行到底。
8. 幂等：已是同版本则跳过下载，直接进入第 7 步。

### 7.4 `scripts/install.ps1`（PowerShell 5.1+ / 7）

1. `$env:PROCESSOR_ARCHITECTURE` → `amd64|arm64`。
2. 版本同上，`$env:RCHROME_VERSION`。
3. `Invoke-WebRequest` 下载 zip 与 `checksums.txt`，`Get-FileHash -Algorithm SHA256` 校验。
4. 解压到 `%LOCALAPPDATA%\rchrome\`，把该目录写入**用户级** PATH（`[Environment]::SetEnvironmentVariable(..., 'User')`），同时追加到当前会话 `$env:PATH`，本窗口立刻可用。
5. `Unblock-File` 去掉下载标记。
6. `$env:RCHROME_RUN` 非空时，安装后直接 `& rchrome $env:RCHROME_RUN`。

### 7.5 其它渠道（可选，脚本之外）

Homebrew tap `ceeyang/tap`、Scoop bucket、`go install github.com/ceeyang/rchrome/cmd/rchrome@latest`。它们都不是主路径，README 里主推 §3.0 的一行脚本。

---

## 8. 测试与验收

### 8.1 单元测试（`go test ./...`，无网络）

- `chrome.Find`：用临时目录伪造各平台文件布局，验证查找顺序与兜底。
- `mcp.RegisterCommand`：验证生成的 `claude mcp add` 参数、Windows `npx.cmd`、绝对路径。
- `config`：读写 `connect.json`、权限 0600。
- `tunnel.pipe`：双向拷贝、一端关闭另一端随之关闭。

### 8.2 集成测试（build tag `integration`，需要外网访问 DERP）

同一进程内：`httptest.Server` 伪装 Chrome 返回 `/json/version` → `tunnel.Server` 的 OnTCP 指向它 → `tunnel.Client` 拨号 → 通过隧道 `GET /json/version` → 断言 body 一致。再启动 `rchrome mcp` 子命令（指向伪 CDP），用 `--backend` 换成一个 echo 脚本，验证 stdin / stdout 透传与退出码传递。

### 8.3 人工验收（发版前必做）

| # | 场景 | 通过标准 |
|---|---|---|
| A1 | 远程 Windows 11 接手机热点，本机 macOS 接家用 Wi-Fi | `serve` → `connect` → 新开 Claude Code，说「打开 example.com 并截图」，截图返回且远程屏幕上 Chrome 确实打开了页面 |
| A2 | 远程 macOS、本机 Linux | 同上 |
| A3 | 关闭 Claude Code 会话 | `rchrome mcp` 进程消失，`lsof -i :9222` 为空 |
| A4 | 远程 Ctrl+C 再 `rchrome serve` | 打出的地址与上次完全相同；本机无需重新 connect，新会话直接可用 |
| A5 | 本机 9222 已被占用 | connect 提示换到了别的端口，功能正常 |
| A6 | 远程 serve 没开 | connect 3 秒内给出 ✗ 和明确提示，退出码 3 |
| A7 | 本机没装 Node | connect 阶段就报退出码 4 和安装提示，而不是等到 Claude Code 里才失败 |
| A8 | `--allow` 指定了别的公钥 | 本机连接被拒，serve 侧有日志 |
| A9 | 远程是只有 Safari 的 Mac，`serve --safari` + `connect --backend safari` | 新会话里出现 `remote-safari__*` 工具，「打开 example.com 截图」成功，远程 Mac 弹出 Safari 自动化窗口 |
| A10 | 远程 serve 未运行时新开 Claude Code | `remote-chrome__*` 工具仍在列表；调用时 Claude 转述「远程主机的 rchrome serve 未运行」 |
| A11 | 本机同时连着 Claude in Chrome 扩展和本地 Playwright MCP | 对 Claude 说「用远程那台电脑的 Chrome 打开 example.com」，它选 `remote-chrome__*`；Claude Code `/mcp` 面板里 remote-chrome 的说明显示远程主机信息 |
| A12 | `--backend both` | `remote-chrome` 与 `remote-chrome-pw` 两组工具都能截图 |
| A13 | 全新 Windows 11 虚拟机，无 Git / Go / Node，只有 Chrome | 在 PowerShell 粘贴 `$env:RCHROME_RUN='serve'; irm …/install.ps1 \| iex` 一行，60 秒内打出地址；全新 macOS 同理用 `curl … \| sh -s -- serve` |
| A14 | 本机已装旧版 rchrome | 重跑安装脚本升级到最新，`rchrome version` 正确；已注册的 MCP 继续可用（因为注册的是绝对路径且路径不变） |

---

## 9. 安全说明（原样写进 README）

- **地址串就是密钥。** 拿到地址串的人可以完全操作远程 Chrome，包括其中已登录的网站。不要贴到公开群组。要更保险，用 `rchrome keygen` + `serve --allow`。
- **调试端口没有鉴权。** rchrome 只把它暴露给隧道对端，Chrome 本身仍只监听 127.0.0.1，不会暴露到局域网。
- **独立 profile。** `serve` 启动的 Chrome 用 `~/.rchrome/profile`，不碰用户日常 Chrome 的 cookie。但用户若在这个 profile 里登录了网站，这些登录态对隧道对端可见。
- **公共中继只看到密文。** 打洞失败时流量经 Tailscale 公共 DERP 中继，端到端 WireGuard 加密，中继无法解密。中继有限速，仅作兜底。
- **用完就关。** 不需要时 Ctrl+C 结束 `serve`。

---

## 10. 后续版本

- v1.1：`rchrome keygen` + 客户端持久身份；`--allow` 完整化；`serve --qr`；**局域网自动发现**：`serve` 用 mDNS（`_rchrome._tcp`）广播地址，`connect` 不带参数时扫同网段并列出可选项。跨互联网做不到零输入发现，首次仍需一次地址交换，这是密码学限制。
- v1.2：多远程主机（`connect --name win-office`，各自注册一个 MCP）。
- v1.3：常驻：`serve --install-service`（Windows 用 sc.exe / nssm，macOS launchd，Linux systemd user unit）。
- v2：Safari 操作用户当前已登录的标签页。safaridriver 的会话是独立 profile，Apple 若开放相关能力再跟进。

---

## 附录 A：tailcat 速查

- 模块：`github.com/tailscale/tailcat`，BSD-3-Clause。既是 CLI 也是库。
- 服务端：`tailcat.Server{OnTCP: func(port uint16) func(net.Conn)}`，`Start()` / `StartContext(ctx)`，`TailcatAddr() Addr`，`Close()`，`AllowClient`（`KeySet`）。
- 客户端：`tailcat.NewClient(tailcat.Addr) *Client`，`DialTCPPort(ctx, port)`，`Ping`，`DERPRegion`，`Close()`；字段 `Key key.NodePrivate`、`DERPMapURL`。
- 密钥：`NewPrivateKey() *PrivateKey`、`NewPresharedKey()`；地址解析 `ParseAddr(Addr) (ConnInfo, error)`。
- 地址格式：`tc` + base64(CBOR)，含服务端 WireGuard 公钥、路径发现公钥、预共享密钥、DERP 信息。
- CLI 对照（排障时用）：`tailcat serve 9222` / `tailcat forward tc… 9222:9222` / `tailcat genkey --key=default --fixed-region` / `tailcat serve --allow=nodekey:… 9222`。
- 自建中继：`genkey --region=derp.example.com`，中继用 Tailscale 的 `derper`，v1 不做。

## 附录 B：如果库 API 对不上

tailcat 还年轻，API 可能变。若嵌库受阻，降级方案：rchrome 首次运行把对应平台的 `tailcat` 二进制下载到 `~/.rchrome/bin/`，然后 `exec` 它：

- serve 侧：`tailcat serve --key=rchrome <port>`，从 stderr 匹配 `address: (tc\S+)` 取地址。
- mcp 侧：`tailcat forward tc… <local>:<remote>`，轮询本地端口就绪后再起 MCP 子进程。

用户体验不变，只是多一个下载步骤。

## 附录 C：Playwright + WebKit 内核路线（不是 Safari）

用户坚持要 Playwright 工具集而远程只有 Mac 时的可选路线，不进 v1 默认流程：

1. 远程 Mac：`npx playwright install webkit`，然后 `npx playwright run-server --port 3000 --host 127.0.0.1`。
2. 远程 Mac：`rchrome serve --port 3000 --no-launch`，把 3000 搬出去。
3. 本机：`rchrome connect tc… --backend playwright-server`，注册的 MCP 命令为 `npx -y @playwright/mcp@latest --endpoint ws://127.0.0.1:3000/`。

得到的是 Playwright 自带的 WebKit 内核，与 Safari 同源但不是 Safari 应用：没有它的登录态、扩展和系统集成。适合验证 WebKit 渲染，不适合「操作那台机器上的 Safari」。实现成本低，只是另一个 backend 字串，可作 v1 隐藏选项。

## 附录 D：参考

- tailcat 仓库与 README：https://github.com/tailscale/tailcat
- tailcat 发布博文：https://tailscale.com/blog/tailcat
- Chrome DevTools MCP：https://github.com/ChromeDevTools/chrome-devtools-mcp
- Playwright MCP（`--cdp-endpoint`）：https://github.com/microsoft/playwright-mcp
- Chrome 136+ 调试端口须配非默认 user-data-dir：https://developer.chrome.com/blog/remote-debugging-port
- Claude Code MCP 配置：https://code.claude.com/docs/en/mcp
