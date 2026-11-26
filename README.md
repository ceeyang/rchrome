# rchrome

一条命令，让本机的 Claude Code 直接操控远程主机上的 Chrome。

**English TL;DR** — Drive a Chrome running on another machine from your local Claude Code with two commands: `rchrome serve` on the remote box, `rchrome connect <addr>` locally. No accounts, no public IP, no port forwarding, no root. Built on Chrome's DevTools Protocol plus [tailcat](https://github.com/tailscale/tailcat) (Tailscale's open-source data plane: WireGuard + NAT traversal + relay fallback) embedded as a Go library. Full design in [SPEC.md](SPEC.md).

## 它解决什么

Claude Code 自带的 Claude in Chrome 扩展可以跨机器，但要求在另一台机器上登录同一个 Claude 账号。云浏览器要账号要付费，而且不是你自己的那台机器。手工方案（开调试端口 + SSH 隧道 + 改 MCP 配置）步骤多，两台机器还经常不在同一网络。

rchrome 把这些压成两条命令。

## 安装

一行命令，不需要 Git、Go 或编译。两台机器都是这一行：

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

本机侧另需 Node.js 20+（官方 chrome-devtools-mcp 通过 npx 运行）；远程主机不需要。

## 用法

远程主机（Windows / macOS / Linux）：

```bash
rchrome serve
# 🐈 地址（持久，重启不变）
#    tcomFwWCCcjS5nKNqAod034nWoJZW0LZqDhhC8U_dKdnDRYQ8uNGFpGQEu
```

本机：

```bash
rchrome connect tcomFwWCCcjS5nKNqAod034nWoJZW0LZqDhhC8U_dKdnDRYQ8uNGFpGQEu
# ✓ 已连上远程 Chrome   Chrome/142.0.7444.59   Windows
# ✓ 已注册到 Claude Code   名称 remote-chrome
```

之后新开一个 Claude Code 会话，让它「打开 example.com 并截图」。每次会话由 Claude Code 自动拉起 `rchrome mcp`，它自己建隧道、起官方 chrome-devtools-mcp，会话结束自动断开。远程只要 `rchrome serve` 还在跑即可。

**Claude 不需要任何提示就能认出它。** 工具列表里会出现 `remote-chrome__*` 一组工具，MCP 握手说明里写明「这是远程主机 Windows 上的 Chrome，不是本机浏览器」。想用 Playwright 的工具集就 `connect --backend playwright`，`--backend both` 两套都要。

远程是只有 Safari 的 Mac：

```bash
rchrome serve --safari                 # 远程 Mac，走 Apple 官方 safaridriver --mcp（Safari 27+）
rchrome connect tc… --backend safari   # 本机
```

Playwright 连不上真 Safari（它的 webkit 是自带内核，`--backend playwright` 只对 Chrome / Edge 有效），所以 Safari 走 Apple 自己的 MCP。

## 原理

```
远程 Chrome --remote-debugging-port=9222 (127.0.0.1)
    ▲
rchrome serve ── tailcat Server ══ WireGuard 隧道 ══ tailcat Client ── rchrome mcp
                                                                         └─ npx chrome-devtools-mcp --browserUrl http://127.0.0.1:9222
                                                                              ▲
                                                                         Claude Code
```

不自造浏览器协议：远程端是 Chrome 原生 CDP，本机端复用 Google 官方 chrome-devtools-mcp 或微软 @playwright/mcp。rchrome 只负责把远程的 9222 安全地搬到本机的 127.0.0.1。

## 状态

当前仓库只有完整规格 [SPEC.md](SPEC.md)：命令、flag、文案、退出码、各平台 Chrome 查找、tailcat 嵌入方式、仓库结构、发版、测试与验收标准都已写定。把 SPEC.md 交给任何 AI 编码助手或开发者即可按规格实现。欢迎 PR。

## 安全

- 地址串就是密钥，拿到的人可以完全操作远程 Chrome。不要贴到公开群组。
- 调试端口只监听远程机的 127.0.0.1，不暴露到局域网；只有隧道对端能到达。
- `serve` 启动的 Chrome 使用独立 profile，不碰日常 Chrome 的登录态。
- 打洞失败时走 Tailscale 公共中继，端到端加密，中继只见密文。
- 不用时 Ctrl+C 结束 `serve`。

## License

MIT
