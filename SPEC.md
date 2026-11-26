# rchrome：一条命令让本机的 Claude 操控远程主机的 Chrome

> 本文是给**实现者**（AI 编码助手或开发者）的完整规格。读完即可从零建仓库、实现、测试、发版。
> 最终用户是开发者，但对他们的要求必须是：两端各敲一条命令，不注册账号、不配路由器、不改防火墙、不需要 root。

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
| G8 | 单二进制，GitHub Releases 提供各平台产物，附一行安装脚本 |

### 非目标（v1 不做，留到 §10）

Safari、同时连多台远程机、Web 管理界面、Windows 服务 / systemd 常驻、二维码、GUI。

---

## 3. 用户体验（输出文案必须与此一致）

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

### 3.2 本机

```
$ rchrome connect tcomFwWCCcjS5nKNqAod034nWoJZW0LZqDhhC8U_dKdnDRYQ8uNGFpGQEu
✓ 已连上远程 Chrome   Chrome/142.0.7444.59   Windows
✓ 已注册到 Claude Code   名称 rchrome   后端 chrome-devtools-mcp
新开一个 Claude Code 会话，对它说「打开 example.com 并截图」试试。
```

### 3.3 之后

用户只需保证远程主机的 `rchrome serve` 还在跑。本机每次新开 Claude Code 会话，Claude Code 启动 MCP 子进程 `rchrome mcp`，它自己拉隧道、起 chrome-devtools-mcp；会话结束，子进程退出，隧道随之关闭。

---

## 4. 命令规格

所有子命令：`--help`、`--verbose`（隧道日志到 stderr）、`--json`（机器可读输出，给其他 AI 调用用）。

### 4.1 `rchrome serve`

| flag | 默认 | 说明 |
|---|---|---|
| `--port` | 9222 | Chrome 调试端口，也是隧道里暴露的端口号 |
| `--profile` | `~/.rchrome/profile` | Chrome user-data-dir。**必须**是非默认目录，Chrome 136+ 拒绝在默认 profile 上开调试端口 |
| `--chrome <path>` | 自动查找 | 指定浏览器可执行文件 |
| `--no-launch` | false | 不启动 Chrome，只把已经在跑的 `127.0.0.1:<port>` 搬出去 |
| `--keep-chrome` | false | Ctrl+C 时不关 Chrome |
| `--allow <nodekey>` | 空 | 只允许这些客户端公钥连接，可多次 |
| `--headless` | false | 以 `--headless=new` 启动 Chrome |
| `--key new` | 使用持久密钥 | 本次用一次性密钥（地址一次性） |

行为：
1. 查找 Chrome（§5.2）。找不到 → 退出码 2，提示 `--chrome`。
2. 若 `--no-launch` 为假：启动 Chrome，轮询 `http://127.0.0.1:<port>/json/version` 最多 20 秒，拿到 `Browser` 字段。
3. 加载或创建持久密钥（§5.3），启动 tailcat Server，仅接受发往 `<port>` 的连接，其余端口直接关闭。
4. 打印 §3.1 文案。`--json` 时输出 `{"addr":"tc…","port":9222,"browser":"Chrome/142…","os":"windows"}`。
5. 阻塞直到 SIGINT / SIGTERM：关闭 Server，按 `--keep-chrome` 决定是否结束 Chrome（Windows 上要结束整棵进程树）。

### 4.2 `rchrome connect <addr>`

| flag | 默认 | 说明 |
|---|---|---|
| `--backend` | `devtools` | `devtools` = chrome-devtools-mcp；`playwright` = @playwright/mcp；`both` = 注册两个 |
| `--name` | `rchrome` | 注册进 Claude Code 的 MCP 名称 |
| `--scope` | `user` | 传给 `claude mcp add -s` |
| `--no-register` | false | 不动 Claude Code，只打印可粘贴的 JSON 配置（给 Cursor、Codex 等其他客户端） |
| `--local-port` | 9222 | 本机监听端口；被占用时自动换空闲端口并提示 |

行为：
1. 解析地址，起 tailcat Client，`DialTCPPort(remotePort)`，通过隧道 `GET /json/version`（Host 头写 `127.0.0.1:<port>`），解析 `Browser` 和 `User-Agent` 得到版本、系统。失败 → 退出码 3，提示「远程主机是否在跑 rchrome serve」。
2. 写 `~/.rchrome/connect.json`：`{"addr":"tc…","remote_port":9222,"local_port":9222,"backend":"devtools"}`。
3. 注册到 Claude Code（§5.6）。
4. 打印 §3.2 文案。

### 4.3 `rchrome mcp [addr]`

Claude Code 实际调用的命令，用户一般不直接敲。

1. `addr` 省略时读 `~/.rchrome/connect.json`。
2. 起 tailcat Client；本机 `net.Listen("tcp", "127.0.0.1:<local_port>")`，被占用就换空闲端口。每个 accept 到的连接 → `client.DialTCPPort(ctx, remotePort)` → 双向 `io.Copy`。
3. 启动子进程（§5.5）。**stdin / stdout 原样透传给子进程**，rchrome 自己的任何输出只能写 stderr 或日志文件，否则会污染 MCP 的 JSON-RPC 流。
4. 子进程退出 → 关监听、关 Client → 以子进程的退出码退出。收到 SIGINT / SIGTERM → 转发给子进程，等待最多 5 秒后强杀。

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
    "rchrome": {
      "command": "/abs/path/rchrome",
      "args": ["mcp", "tc…", "--backend", "devtools"]
    }
  }
}
```

### 5.7 配置目录 `~/.rchrome/`

```
server.key.json   服务端持久私钥（0600）
client.key.json   客户端私钥（可选）
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

退出码：0 成功；2 找不到 Chrome；3 隧道连接失败；4 缺 Node / npx；5 Claude CLI 注册失败；1 其它。

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

- GoReleaser，`CGO_ENABLED=0`，目标：`darwin/arm64`、`darwin/amd64`、`windows/amd64`、`windows/arm64`、`linux/amd64`、`linux/arm64`。
- `-ldflags "-s -w -X main.version=… -X main.commit=…"`，`version` 子命令读取。
- `install.sh`：探测 OS / arch → 下载对应 tar.gz → 校验 `checksums.txt` → 放到 `/usr/local/bin`（无权限则 `~/.local/bin` 并提示加 PATH）。
- `install.ps1`：下载 zip → 解压到 `%LOCALAPPDATA%\rchrome` → 写入用户 PATH。
- 可选：Homebrew tap、Scoop bucket。

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

---

## 9. 安全说明（原样写进 README）

- **地址串就是密钥。** 拿到地址串的人可以完全操作远程 Chrome，包括其中已登录的网站。不要贴到公开群组。要更保险，用 `rchrome keygen` + `serve --allow`。
- **调试端口没有鉴权。** rchrome 只把它暴露给隧道对端，Chrome 本身仍只监听 127.0.0.1，不会暴露到局域网。
- **独立 profile。** `serve` 启动的 Chrome 用 `~/.rchrome/profile`，不碰用户日常 Chrome 的 cookie。但用户若在这个 profile 里登录了网站，这些登录态对隧道对端可见。
- **公共中继只看到密文。** 打洞失败时流量经 Tailscale 公共 DERP 中继，端到端 WireGuard 加密，中继无法解密。中继有限速，仅作兜底。
- **用完就关。** 不需要时 Ctrl+C 结束 `serve`。

---

## 10. 后续版本

- v1.1：`rchrome keygen` + 客户端持久身份；`serve --qr` 打二维码；`--allow` 完整化。
- v1.2：多远程主机（`connect --name win-office`，各自注册一个 MCP）。
- v1.3：常驻：`serve --install-service`（Windows 用 sc.exe / nssm，macOS launchd，Linux systemd user unit）。
- v2：Safari。Safari 没有 CDP，走 `safaridriver --mcp`（Safari 27+ 自带，stdio 协议）：隧道转发远程 Mac 的 22 端口，MCP 命令变为 `ssh -p <local> user@127.0.0.1 /usr/bin/safaridriver --mcp`。

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

## 附录 C：参考

- tailcat 仓库与 README：https://github.com/tailscale/tailcat
- tailcat 发布博文：https://tailscale.com/blog/tailcat
- Chrome DevTools MCP：https://github.com/ChromeDevTools/chrome-devtools-mcp
- Playwright MCP（`--cdp-endpoint`）：https://github.com/microsoft/playwright-mcp
- Chrome 136+ 调试端口须配非默认 user-data-dir：https://developer.chrome.com/blog/remote-debugging-port
- Claude Code MCP 配置：https://code.claude.com/docs/en/mcp
