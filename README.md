# rchrome

一条命令，让本机的 Claude Code 直接操控远程主机上的 Chrome（或 Safari）。

**English TL;DR** — Drive a Chrome running on another machine from your local Claude Code with two commands: `rchrome serve` on the remote box, `rchrome connect <addr>` locally. No accounts, no public IP, no port forwarding, no root. Chrome's DevTools Protocol over [tailcat](https://github.com/tailscale/tailcat) (Tailscale's open-source data plane: WireGuard + NAT traversal + relay fallback) embedded in a single Go binary. Safari via Apple's `safaridriver --mcp`. Design in [SPEC.md](SPEC.md).

## 安装（一行，不需要 Git / Go / 编译）

```bash
# macOS / Linux
curl -fsSL https://raw.githubusercontent.com/ceeyang/rchrome/main/scripts/install.sh | sh
```

```powershell
# Windows PowerShell
irm https://raw.githubusercontent.com/ceeyang/rchrome/main/scripts/install.ps1 | iex
```

远程主机想一行到底（装完直接开始服务）：

```bash
curl -fsSL https://raw.githubusercontent.com/ceeyang/rchrome/main/scripts/install.sh | sh -s -- serve
```
```powershell
$env:RCHROME_RUN='serve'; irm https://raw.githubusercontent.com/ceeyang/rchrome/main/scripts/install.ps1 | iex
```

脚本从 GitHub Releases 下载对应平台的单文件二进制，sha256 校验后放进 PATH。本机侧另需 Node.js 20+（官方 chrome-devtools-mcp 通过 npx 运行），远程主机不需要。

## 用法

远程主机（Windows / macOS / Linux）：

```
$ rchrome serve
🐈 Chrome/142.0.7444.59 已启动   调试端口 9222   中继 Tokyo
🐈 地址（持久，重启不变）
   tcpGFwWCCw3PY83iIXXiEknBhGp-8nonNJz…

在本机运行：
   rchrome connect tcpGFwWCCw3PY83iIXXiEknBhGp-8nonNJz…

Ctrl+C 结束（会一起关闭 Chrome）
```

本机：

```
$ rchrome connect tcpGFwWCCw3PY83iIXXiEknBhGp-8nonNJz… --name win-office --label "办公室的 Windows"
✓ 已连上远程浏览器   Chrome/142.0.7444.59   Windows
✓ 已注册到 Claude Code   名称 win-office（devtools）   备注 办公室的 Windows
本机身份 nodekey:8195…e43d   （远程可用 rchrome serve --allow <此值> 只放行这台机器）

新开一个 Claude Code 会话，对它说「用 办公室的 Windows 打开 example.com 并截图」试试。
```

之后不用再碰 rchrome。每次新开 Claude Code 会话，它自动拉起 `rchrome mcp`，后者自己建隧道、起官方 chrome-devtools-mcp，会话结束自动断开。远程只要 `rchrome serve` 还在跑即可。

## Claude 怎么认出它、怎么区分多台机器

- `--name` 是 MCP 名称，也是工具前缀：`win-office__take_screenshot`、`mac-home__navigate_page`。
- `--label` 是备注，连同远程系统和浏览器版本一起注入 MCP 握手说明。Claude 看到的是「这组工具操作的是远程主机『办公室的 Windows』（Windows, Chrome/142）上的浏览器，不是本机浏览器」，所以即使本机同时有 Claude in Chrome 扩展和本地 Playwright，它也能选对。
- 多台远程主机：各 `serve` 一次、各 `connect` 一次，用不同 `--name` 即可。`rchrome list` 查看。
- 远程 `serve` 没在跑时，工具仍在列表里；Claude 调用时会收到「远程主机的 rchrome serve 未运行」并原话转告你。

## 后端

| `connect --backend` | 本机跑的 MCP | 适用 |
|---|---|---|
| `devtools`（默认） | Google 官方 chrome-devtools-mcp | Chrome / Edge / Chromium |
| `playwright` | 微软 @playwright/mcp（`--cdp-endpoint`） | Chrome / Edge / Chromium |
| `both` | 两个都注册（`<name>` 与 `<name>-pw`） | 想两套工具集都要 |
| `safari` | 直通远程 Mac 的 `safaridriver --mcp` | 远程只有 Safari（27+） |

Playwright 连不上真 Safari（它的 webkit 是自带内核），所以 Safari 走 Apple 自己的 MCP：

```bash
rchrome serve --safari                      # 远程 Mac；先在 Safari → 开发 里勾「允许远程自动化和外部代理」
rchrome connect tc… --backend safari        # 本机
```

## 子命令

```
rchrome serve     [--port 9222] [--headless] [--no-launch] [--keep-chrome] [--allow nodekey:…] [--safari] [--key new]
rchrome connect   <tc地址> [--name N] [--label 备注] [--backend devtools|playwright|both|safari] [--scope user|project|local] [--no-register]
rchrome list                                   已连接的远程主机
rchrome forward   [tc地址] [--local-port 9222]  纯端口转发，给 Playwright 脚本 connectOverCDP 用
rchrome doctor                                 检查 Chrome / Node / claude CLI / 各远程是否可达
rchrome keygen                                 打印本机身份，给远程 serve --allow 用
```

## 原理

```
远程 Chrome --remote-debugging-port (127.0.0.1)
    ▲
rchrome serve ── tailcat Server ══ WireGuard 隧道（打洞，失败走 DERP 中继）══ tailcat Client ── rchrome mcp
                                                                                            ├─ 127.0.0.1:9222 本地监听
                                                                                            └─ npx chrome-devtools-mcp --browserUrl http://127.0.0.1:9222
                                                                                                 ▲ stdio（握手时注入「这是远程浏览器」说明）
                                                                                            Claude Code
```

不自造浏览器协议：远程端是 Chrome 原生 CDP，本机端复用官方 MCP。rchrome 只负责把远程的调试端口安全地搬到本机的 127.0.0.1，并在 MCP 握手里告诉 Claude 这是哪台机器。

## 安全

- 地址串就是密钥，拿到的人可以完全操作远程浏览器。不要贴到公开群组。要更保险：本机 `rchrome keygen`，远程 `rchrome serve --allow nodekey:…`。
- 调试端口只监听远程机的 127.0.0.1，不暴露到局域网；只有隧道对端能到达。
- `serve` 启动的 Chrome 使用独立 profile（`~/.rchrome/profile`），不碰日常 Chrome 的登录态；但你在这个 profile 里登录的网站，隧道对端能操作。
- 打洞失败时走 Tailscale 公共 DERP 中继，端到端 WireGuard 加密，中继只见密文。
- 不用时 Ctrl+C 结束 `serve`。

## 状态

v0.1 已实现并在 macOS 上实测：serve → connect → Claude Code 里 navigate + screenshot 全链路通过。Windows / Linux 二进制由 CI 交叉编译，欢迎反馈。设计文档 [SPEC.md](SPEC.md)。

## License

MIT
