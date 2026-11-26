// Command rchrome lets a local Claude Code drive Chrome (or Safari) on a remote
// machine through an account-free, NAT-traversing tunnel.
//
//	remote$ rchrome serve
//	local$  rchrome connect <tc-address>
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ceeyang/rchrome/internal/chrome"
	"github.com/ceeyang/rchrome/internal/config"
	"github.com/ceeyang/rchrome/internal/mcp"
	"github.com/ceeyang/rchrome/internal/tunnel"
)

var (
	version = "dev"
	commit  = ""
)

const (
	defaultPort       = 9222
	safariPort        = 4445
	defaultName       = "remote-chrome"
	defaultSafariName = "remote-safari"
	repoURL           = "https://github.com/ceeyang/rchrome"
)

const usageText = `rchrome — 一条命令让本机 Claude Code 操控远程主机的 Chrome

  远程主机   rchrome serve [--safari]                 启动 Chrome 并打出地址（tc…）
  本机       rchrome connect <地址> [--name N --label 备注]   连通并注册到 Claude Code
  查看       rchrome list                              已连接的远程主机
  其它       rchrome forward <地址> | doctor | keygen | version

多台远程主机：各自 connect 一次，用 --name 区分（工具前缀），--label 写备注给 Claude 看。
子命令后加 --help 看全部参数。  ` + repoURL + `
`

// exit codes (see SPEC §5.9)
const (
	codeOK       = 0
	codeGeneric  = 1
	codeNoChrome = 2
	codeTunnel   = 3
	codeNoNode   = 4
	codeRegister = 5
	codeSafari   = 6
)

func main() {
	log.SetFlags(0)
	args := os.Args[1:]
	verbose := false
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "--verbose", "-v":
			verbose = true
		case "-h", "--help", "help":
			fmt.Print(usageText)
			return
		case "--version":
			printVersion()
			return
		default:
			fmt.Fprintf(os.Stderr, "未知参数 %s\n\n%s", args[0], usageText)
			os.Exit(codeGeneric)
		}
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Print(usageText)
		os.Exit(codeGeneric)
	}
	sub, rest := args[0], args[1:]
	var (
		code int
		err  error
	)
	switch sub {
	case "serve":
		code, err = runServe(rest, verbose)
	case "connect":
		code, err = runConnect(rest, verbose)
	case "mcp":
		code, err = runMCP(rest, verbose)
	case "forward":
		code, err = runForward(rest, verbose)
	case "list", "ls":
		code, err = runList(rest)
	case "doctor":
		code, err = runDoctor(rest, verbose)
	case "keygen":
		code, err = runKeygen(rest)
	case "version":
		printVersion()
	case "help", "-h", "--help":
		fmt.Print(usageText)
	default:
		fmt.Fprintf(os.Stderr, "未知子命令 %q\n\n%s", sub, usageText)
		code = codeGeneric
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(codeOK)
		}
		fmt.Fprintln(os.Stderr, "✗", err)
		if code == codeOK {
			code = codeGeneric
		}
	}
	os.Exit(code)
}

func printVersion() {
	v := version
	if commit != "" {
		v += " (" + commit + ")"
	}
	fmt.Printf("%s %s/%s tailcat-embedded\n", v, runtime.GOOS, runtime.GOARCH)
}

// ---------------------------------------------------------------- helpers

// parseInterspersed lets flags appear after positional arguments
// (`rchrome connect tc… --backend safari`).
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// newLogger returns a logger that always appends to logFile (if non-empty)
// and also writes to stderr when verbose. Never stdout: MCP owns stdout.
func newLogger(verbose bool, logFile string) (tunnel.Logf, io.Writer, func()) {
	var ws []io.Writer
	var closers []io.Closer
	if logFile != "" {
		if f, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			ws = append(ws, f)
			closers = append(closers, f)
		}
	}
	if verbose {
		ws = append(ws, os.Stderr)
	}
	w := io.Writer(io.Discard)
	if len(ws) > 0 {
		w = io.MultiWriter(ws...)
	}
	logf := func(format string, a ...any) {
		msg := strings.TrimRight(fmt.Sprintf(format, a...), "\n")
		// tailcat/wireguard chatter ([v1], [v2], wg:) only matters when debugging.
		if !verbose && (strings.HasPrefix(msg, "[v") || strings.HasPrefix(msg, "wg: ") || strings.Contains(msg, "[v2]") || strings.Contains(msg, "[v1]")) {
			return
		}
		fmt.Fprintf(w, "%s %s\n", time.Now().Format("15:04:05.000"), msg)
	}
	return logf, w, func() {
		for _, c := range closers {
			_ = c.Close()
		}
	}
}

func logPath(prefix string) string {
	d, err := config.LogsDir()
	if err != nil {
		return ""
	}
	return d + string(os.PathSeparator) + prefix + "-" + time.Now().Format("20060102") + ".log"
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func buildInstructions(name, label, desc string) string {
	if label == "" {
		label = name
	}
	if desc == "" {
		desc = "远程主机"
	}
	return fmt.Sprintf(
		"这组工具（前缀 %s）操作的是远程主机「%s」（%s）上的浏览器，经 rchrome 隧道转发，不是本机浏览器。"+
			"用户提到「%s」、「远程」、「那台电脑」或远程主机上的浏览器时用这组工具；用户要操作本机浏览器时不要用。"+
			"如果工具报 Could not connect / Service Unavailable / remote rchrome serve is not running，意思是远程主机上的 rchrome serve 没在运行，请原话告诉用户去远程主机执行 rchrome serve，不要自己重试很多次。"+
			"远程屏幕只会显示前台标签页：Chrome 不绘制后台标签页，所以在后台页上导航、点击、填表都会生效但用户在远程屏幕上看不到。要让用户看到过程，先用 select_page 把目标页切到前台（它会 bringToFront），新开页用 new_page 时不要加 background。\n"+
			"These tools (prefix %s) control the browser on the REMOTE machine %q (%s) through the rchrome tunnel, not the local browser. "+
			"Use them when the user refers to %q, \"remote\", or \"that computer\". "+
			"An error mentioning 'remote rchrome serve is not running' means the remote side is down: tell the user to run `rchrome serve` there. "+
			"Chrome only paints the foreground tab: actions on a background tab still work but are invisible on the remote screen, so call select_page first when the user wants to watch.",
		name, label, desc, label, name, label, desc, label)
}

// ---------------------------------------------------------------- serve

func runServe(args []string, verbose bool) (int, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "用法: rchrome serve [flags]\n在远程主机上运行：启动 Chrome（独立 profile、调试端口）并把它通过加密隧道暴露给本机。\n")
		fs.PrintDefaults()
	}
	port := fs.Int("port", defaultPort, "Chrome 调试端口（被占用时自动换一个；隧道对外固定暴露 9222，本机 connect 不用改）")
	profile := fs.String("profile", "", "Chrome user-data-dir（默认 ~/.rchrome/profile；Chrome 136+ 不允许用默认 profile）")
	chromePath := fs.String("chrome", "", "浏览器可执行文件路径（默认自动查找 Chrome / Edge）")
	noLaunch := fs.Bool("no-launch", false, "不启动 Chrome，只转发已在 127.0.0.1:<port> 运行的调试端口")
	keepChrome := fs.Bool("keep-chrome", false, "退出时不关闭 Chrome")
	headless := fs.Bool("headless", false, "以 --headless=new 启动 Chrome")
	keyMode := fs.String("key", "", "'new' = 本次用一次性密钥（地址一次性）；默认用持久密钥，地址固定")
	safari := fs.Bool("safari", false, "macOS：不启动 Chrome，改为通过隧道暴露 safaridriver --mcp（Safari 27+）")
	attach := fs.Bool("attach", false, "不新开 profile：接管这台机器上正在运行、已登录的 Chrome（需先在它的 chrome://inspect/#remote-debugging 勾选 Allow remote debugging，Chrome 144+）；--profile 可指定 user-data-dir")
	jsonOut := fs.Bool("json", false, "机器可读输出")
	var allow multiFlag
	fs.Var(&allow, "allow", "只允许这些客户端 nodekey:… 连接（可多次；本机用 rchrome keygen 查看）")
	fs.BoolVar(&verbose, "verbose", verbose, "隧道日志输出到 stderr")
	if _, err := parseInterspersed(fs, args); err != nil {
		return codeGeneric, err
	}

	logf, logw, closeLog := newLogger(verbose, logPath("serve"))
	defer closeLog()
	ctx, cancel := signalContext()
	defer cancel()

	var (
		handler     func(net.Conn, string)
		browserDesc string
		tunnelPort  = uint16(defaultPort) // what clients dial; independent of the real Chrome port
		chromePort  = *port
		inst        *chrome.Instance
	)
	if *attach && *safari {
		return codeGeneric, errors.New("--attach 和 --safari 不能同时用 / mutually exclusive")
	}
	if *attach {
		a, err := chrome.FindAttach(*profile)
		if err != nil {
			return codeNoChrome, err
		}
		browserDesc = a.Browser() + "（已登录的现有 Chrome，profile " + a.UserDataDir + "）"
		h := chrome.AttachHandler(a.UserDataDir, logf)
		handler = func(c net.Conn, peer string) { h(c) }
		logf("attach: %s port %d ws %s", a.UserDataDir, a.Port, a.WSPath)
	} else if *safari {
		if runtime.GOOS != "darwin" {
			return codeSafari, errors.New("--safari 只支持 macOS / --safari is macOS only")
		}
		sv, err := checkSafaridriver()
		if err != nil {
			return codeSafari, err
		}
		tunnelPort = safariPort
		browserDesc = sv
		handler = safariHandler(logf, logw)
	} else {
		if !*noLaunch {
			exe := *chromePath
			if exe == "" {
				var err error
				if exe, err = chrome.Find(); err != nil {
					return codeNoChrome, err
				}
			}
			prof := *profile
			if prof == "" {
				var err error
				if prof, err = config.ProfileDir(); err != nil {
					return codeGeneric, err
				}
			}
			if !portFree(chromePort) {
				fp, err := freePort()
				if err != nil {
					return codeGeneric, err
				}
				logf("port %d is busy, launching Chrome on %d instead", chromePort, fp)
				if !*jsonOut {
					fmt.Printf("… 端口 %d 已被占用（可能是你日常的 Chrome），改用 %d 启动\n", chromePort, fp)
				}
				chromePort = fp
			}
			var err error
			if inst, err = chrome.Launch(exe, chromePort, prof, *headless, logw); err != nil {
				return codeNoChrome, err
			}
			defer func() {
				if !*keepChrome {
					inst.Kill()
				}
			}()
			logf("chrome pid %d exe %s profile %s", inst.PID(), exe, prof)
		}
		v, err := chrome.WaitReady(ctx, chromePort, 20*time.Second)
		if err != nil {
			if *noLaunch {
				return codeNoChrome, fmt.Errorf("%w\n  提示：--no-launch 需要你先用 --remote-debugging-port=%d --user-data-dir=<非默认目录> 启动 Chrome；端口上若是别的程序会返回 404", err, chromePort)
			}
			return codeNoChrome, err
		}
		browserDesc = v.Browser
		if inst != nil && !*headless {
			bringToFront(inst.PID(), logf)
		}
		target := fmt.Sprintf("127.0.0.1:%d", chromePort)
		handler = func(c net.Conn, peer string) {
			up, err := net.DialTimeout("tcp", target, 5*time.Second)
			if err != nil {
				logf("dial %s failed: %v", target, err)
				_ = c.Close()
				return
			}
			tunnel.Pipe(c, up)
		}
	}

	// Announce each new peer once.
	var seen sync.Map
	announce := func(peer string) {
		if peer == "" {
			return
		}
		if _, loaded := seen.LoadOrStore(peer, true); !loaded && !*jsonOut {
			fmt.Printf("→ 本机已连接  %s\n", peer)
		}
	}
	keyPath, err := config.ServerKeyPath()
	if err != nil {
		return codeGeneric, err
	}
	srv, err := tunnel.StartServer(ctx, tunnel.ServerOptions{
		KeyPath:   keyPath,
		Ephemeral: *keyMode == "new",
		Port:      tunnelPort,
		Allow:     allow,
		Logf:      logf,
		Handler: func(c net.Conn, peer string) {
			announce(peer)
			handler(c, peer)
		},
	})
	if err != nil {
		return codeTunnel, err
	}
	defer srv.Close()

	connectCmd := "rchrome connect " + srv.Addr
	if *safari {
		connectCmd += " --backend safari"
	}
	if *jsonOut {
		out := map[string]any{
			"addr": srv.Addr, "port": tunnelPort, "browser": browserDesc, "os": runtime.GOOS,
			"safari": *safari, "attach": *attach, "relay": srv.RegionName, "persistent": *keyMode != "new", "connect": connectCmd,
		}
		if !*safari && !*attach {
			out["chrome_port"] = chromePort
		}
		_ = json.NewEncoder(os.Stdout).Encode(out)
	} else {
		if *safari {
			fmt.Printf("🐈 safaridriver 已就绪   %s\n   请确认 Safari → 开发 → 已勾选「允许远程自动化和外部代理」\n", browserDesc)
		} else if *attach {
			fmt.Printf("🐈 已接管正在运行的 %s   中继 %s\n   注意：隧道对端能以你当前登录的身份操作这个浏览器\n", browserDesc, srv.RegionName)
		} else {
			fmt.Printf("🐈 %s 已启动   调试端口 %d   中继 %s\n", browserDesc, chromePort, srv.RegionName)
		}
		if *keyMode == "new" {
			fmt.Printf("🐈 地址（一次性，退出后失效）\n   %s\n\n", srv.Addr)
		} else {
			fmt.Printf("🐈 地址（持久，重启不变）\n   %s\n\n", srv.Addr)
		}
		fmt.Printf("在本机运行：\n   %s\n\nCtrl+C 结束", connectCmd)
		if *attach {
			fmt.Print("（不会关闭你的 Chrome）")
		}
		if inst != nil && !*keepChrome {
			fmt.Print("（会一起关闭 Chrome）")
		}
		fmt.Println()
	}
	<-ctx.Done()
	if !*jsonOut {
		fmt.Println("\n正在关闭…")
	}
	return codeOK, nil
}

// bringToFront raises the freshly launched browser window so the user sees it
// instead of their everyday browser. Best effort; only macOS needs help here.
func bringToFront(pid int, logf tunnel.Logf) {
	if runtime.GOOS != "darwin" || pid <= 0 {
		return
	}
	script := fmt.Sprintf(`tell application "System Events" to set frontmost of (first process whose unix id is %d) to true`, pid)
	if out, err := exec.Command("osascript", "-e", script).CombinedOutput(); err != nil {
		logf("bringToFront: %v %s", err, strings.TrimSpace(string(out)))
	}
}

func portFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func checkSafaridriver() (string, error) {
	out, err := exec.Command("/usr/bin/safaridriver", "--help").CombinedOutput()
	if err != nil && len(out) == 0 {
		return "", fmt.Errorf("找不到 /usr/bin/safaridriver: %v", err)
	}
	if !bytes.Contains(out, []byte("--mcp")) {
		return "", errors.New("这台 Mac 的 safaridriver 不支持 --mcp，需要 Safari 27 或更新 / safaridriver lacks --mcp (Safari 27+ required)")
	}
	ver := "Safari"
	if b, err := exec.Command("defaults", "read", "/Applications/Safari.app/Contents/Info.plist", "CFBundleShortVersionString").Output(); err == nil {
		ver = "Safari " + strings.TrimSpace(string(b))
	}
	return ver + "   --mcp 可用", nil
}

// safariHandler bridges one tunnel connection to one `safaridriver --mcp`
// process. safaridriver supports a single session, so a second concurrent
// connection is answered with a JSON-RPC error and closed.
func safariHandler(logf tunnel.Logf, logw io.Writer) func(net.Conn, string) {
	var busy atomic.Bool
	return func(c net.Conn, peer string) {
		defer c.Close()
		if !busy.CompareAndSwap(false, true) {
			_, _ = io.WriteString(c, `{"jsonrpc":"2.0","id":null,"error":{"code":-32000,"message":"safaridriver 已被另一个会话占用，请先关掉其它会话 / safaridriver is busy with another session"}}`+"\n")
			return
		}
		defer busy.Store(false)
		cmd := exec.Command("/usr/bin/safaridriver", "--mcp")
		cmd.Stderr = logw
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return
		}
		if err := cmd.Start(); err != nil {
			logf("safaridriver start: %v", err)
			_, _ = io.WriteString(c, `{"jsonrpc":"2.0","id":null,"error":{"code":-32000,"message":"safaridriver 启动失败 / failed to start safaridriver: `+err.Error()+`"}}`+"\n")
			return
		}
		logf("safaridriver pid %d for %s", cmd.Process.Pid, peer)
		go func() {
			_, _ = io.Copy(stdin, c)
			_ = stdin.Close()
			time.AfterFunc(2*time.Second, func() { _ = cmd.Process.Kill() })
		}()
		_, _ = io.Copy(c, stdout)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		logf("safaridriver session for %s ended", peer)
	}
}

// ---------------------------------------------------------------- connect

func validBackend(b string) bool {
	switch b {
	case "devtools", "playwright", "both", "safari", "playwright-server":
		return true
	}
	return false
}

func mcpArgs(c *config.Connect, backend, mcpVersion string) []string {
	return []string{
		"mcp",
		"--backend", backend,
		"--name", c.Name,
		"--label", c.Label,
		"--desc", strings.TrimSuffix(c.OS+", "+c.Browser, ", "),
		"--local-port", strconv.Itoa(c.LocalPort),
		"--remote-port", strconv.Itoa(c.RemotePort),
		"--mcp-version", mcpVersion,
		c.Addr,
	}
}

func runConnect(args []string, verbose bool) (int, error) {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "用法: rchrome connect <tc地址> [flags]\n在本机运行：验证能连上远程主机，并把它注册成 Claude Code 的一个 MCP 服务器。\n")
		fs.PrintDefaults()
	}
	backend := fs.String("backend", "devtools", "devtools | playwright | both | safari（远程用 serve --safari 时）")
	name := fs.String("name", "", "MCP 名称，也是工具前缀（默认 remote-chrome / remote-safari）；多台远程主机用不同名字区分")
	label := fs.String("label", "", "备注名，注入给 Claude 看（如「办公室的 Windows」）；默认同 --name")
	scope := fs.String("scope", "user", "claude mcp add -s 的作用域：user | project | local")
	noRegister := fs.Bool("no-register", false, "不碰 Claude Code，只打印可粘贴的 JSON 配置")
	localPort := fs.Int("local-port", defaultPort, "本机监听端口（被占用时运行时自动换）")
	remotePort := fs.Int("remote-port", 0, "远程端口（默认 9222，safari 为 4445）")
	mcpVersion := fs.String("mcp-version", "latest", "chrome-devtools-mcp / @playwright/mcp 的 npm 版本")
	jsonOut := fs.Bool("json", false, "机器可读输出")
	fs.BoolVar(&verbose, "verbose", verbose, "隧道日志输出到 stderr")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return codeGeneric, err
	}
	if len(pos) != 1 {
		fs.Usage()
		return codeGeneric, errors.New("需要一个 tc… 地址 / expected exactly one address")
	}
	addr := strings.TrimSpace(pos[0])
	if !validBackend(*backend) {
		return codeGeneric, fmt.Errorf("未知后端 %q（devtools | playwright | both | safari）", *backend)
	}
	if *backend != "safari" && *backend != "playwright-server" {
		if _, err := mcp.NpxPath(); err != nil {
			return codeNoNode, err
		}
	}
	if *remotePort == 0 {
		*remotePort = defaultPort
		if *backend == "safari" {
			*remotePort = safariPort
		}
	}
	if *name == "" {
		*name = defaultName
		if *backend == "safari" {
			*name = defaultSafariName
		}
	}
	if !config.ValidName(*name) {
		return codeGeneric, errors.New("--name 只能用字母、数字、. _ -，且以字母或数字开头 / invalid --name")
	}
	if *label == "" {
		*label = *name
	}

	logf, _, closeLog := newLogger(verbose, logPath("connect"))
	defer closeLog()
	sctx, cancel := signalContext()
	defer cancel()
	ctx, cancel2 := context.WithTimeout(sctx, 40*time.Second)
	defer cancel2()

	keyPath, _ := config.ClientKeyPath()
	cl, err := tunnel.NewClient(addr, keyPath, logf)
	if err != nil {
		return codeTunnel, err
	}
	defer cl.Close()
	if !*jsonOut {
		fmt.Printf("… 正在连接远程主机（首次可能要几秒）\n")
	}
	conn, err := cl.Dial(ctx, uint16(*remotePort))
	if err != nil {
		return codeTunnel, fmt.Errorf("连不上远程主机：请确认那边 rchrome serve 还在运行，地址是否复制完整 / Cannot reach remote: is `rchrome serve` running? (%v)", err)
	}
	var osName, browser string
	if *backend == "safari" {
		browser, err = safariProbe(conn)
		osName = "macOS"
	} else {
		var v *chrome.Version
		v, err = chrome.VersionOverConn(conn, fmt.Sprintf("127.0.0.1:%d", *remotePort))
		if err == nil {
			browser, osName = v.Browser, chrome.OSFromUserAgent(v.UserAgent)
		}
	}
	_ = conn.Close()
	if err != nil {
		return codeTunnel, fmt.Errorf("隧道通了，但远程端口 %d 不像是浏览器调试端口 / tunnel ok but no browser there: %w", *remotePort, err)
	}

	rec := &config.Connect{
		Name: *name, Label: *label, Addr: addr,
		RemotePort: *remotePort, LocalPort: *localPort, Backend: *backend,
		OS: osName, Browser: browser,
	}
	if err := config.SaveConnection(rec); err != nil {
		return codeGeneric, err
	}
	_ = config.SaveConnect(rec)

	exe, err := os.Executable()
	if err != nil {
		return codeGeneric, err
	}
	type reg struct{ name, backend string }
	regs := []reg{{*name, *backend}}
	if *backend == "both" {
		regs = []reg{{*name, "devtools"}, {*name + "-pw", "playwright"}}
	}
	var manual []string
	registered := make([]string, 0, len(regs))
	registerErr := error(nil)
	for _, r := range regs {
		rc := *rec
		rc.Name = r.name
		a := mcpArgs(&rc, r.backend, *mcpVersion)
		if *noRegister {
			manual = append(manual, mcp.ManualJSON(r.name, exe, a))
			continue
		}
		if err := mcp.ClaudeRegister(*scope, r.name, exe, a); err != nil {
			registerErr = err
			manual = append(manual, mcp.ManualJSON(r.name, exe, a))
			continue
		}
		registered = append(registered, r.name+"（"+r.backend+"）")
	}

	if *jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"ok": true, "name": *name, "label": *label, "backend": *backend, "os": osName, "browser": browser,
			"registered": registered, "client_key": cl.PublicKey(), "manual": manual,
		})
	} else {
		fmt.Printf("✓ 已连上远程浏览器   %s   %s\n", browser, osName)
		if len(registered) > 0 {
			fmt.Printf("✓ 已注册到 Claude Code   名称 %s   备注 %s\n", strings.Join(registered, "、"), *label)
		}
		for _, m := range manual {
			fmt.Printf("\n把下面这段加进你的 MCP 客户端配置（Claude Code: ~/.claude.json；Cursor / Codex 同理）：\n%s\n", m)
		}
		fmt.Printf("本机身份 %s   （远程可用 rchrome serve --allow <此值> 只放行这台机器）\n", cl.PublicKey())
		fmt.Printf("\n新开一个 Claude Code 会话，对它说「用 %s 打开 example.com 并截图」试试。\n", *label)
	}
	if registerErr != nil {
		return codeRegister, registerErr
	}
	return codeOK, nil
}

// safariProbe sends an MCP initialize over the connection and reports serverInfo.
func safariProbe(conn net.Conn) (string, error) {
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	req := `{"jsonrpc":"2.0","id":"rchrome-probe","method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"rchrome","version":"` + version + `"}}}` + "\n"
	if _, err := io.WriteString(conn, req); err != nil {
		return "", err
	}
	line, err := bufio.NewReaderSize(conn, 1<<20).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return "", err
	}
	var resp struct {
		Result struct {
			ServerInfo struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		return "", fmt.Errorf("safaridriver 返回了无法解析的内容: %s", strings.TrimSpace(string(line)))
	}
	if resp.Error != nil {
		return "", errors.New(resp.Error.Message)
	}
	name := resp.Result.ServerInfo.Name
	if !strings.HasPrefix(strings.ToLower(name), "safari") {
		name = "Safari " + name
	}
	return strings.TrimSpace(name + " " + resp.Result.ServerInfo.Version), nil
}

// ---------------------------------------------------------------- mcp

func runMCP(args []string, verbose bool) (int, error) {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "用法: rchrome mcp [tc地址] [flags]\nClaude Code 调用的 MCP 入口（stdio）：建隧道并托管 chrome-devtools-mcp / playwright-mcp，或直通远程 safaridriver。\n")
		fs.PrintDefaults()
	}
	backend := fs.String("backend", "", "devtools | playwright | safari | playwright-server")
	name := fs.String("name", "", "MCP 名称（工具前缀）")
	label := fs.String("label", "", "备注名")
	desc := fs.String("desc", "", "远程描述，如 \"Windows, Chrome/142\"")
	localPort := fs.Int("local-port", 0, "本机监听端口（被占用自动换）")
	remotePort := fs.Int("remote-port", 0, "远程端口")
	mcpVersion := fs.String("mcp-version", "latest", "npm 版本")
	fs.BoolVar(&verbose, "verbose", verbose, "日志同时输出到 stderr")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return codeGeneric, err
	}
	// Fill gaps from the saved connection (by name, else the last one).
	var saved *config.Connect
	if *name != "" {
		saved, _ = config.LoadConnection(*name)
	}
	if saved == nil {
		saved, _ = config.LoadConnect()
	}
	addr := ""
	if len(pos) > 0 {
		addr = strings.TrimSpace(pos[0])
	}
	if saved != nil {
		if addr == "" {
			addr = saved.Addr
		}
		if *backend == "" {
			*backend = saved.Backend
		}
		if *name == "" {
			*name = saved.Name
		}
		if *label == "" {
			*label = saved.Label
		}
		if *desc == "" {
			*desc = strings.TrimSuffix(saved.OS+", "+saved.Browser, ", ")
		}
		if *localPort == 0 {
			*localPort = saved.LocalPort
		}
		if *remotePort == 0 {
			*remotePort = saved.RemotePort
		}
	}
	if addr == "" {
		return codeGeneric, config.ErrNoConnect
	}
	if *backend == "" || *backend == "both" {
		*backend = "devtools"
	}
	if *name == "" {
		*name = defaultName
	}
	if *remotePort == 0 {
		*remotePort = defaultPort
		if *backend == "safari" {
			*remotePort = safariPort
		}
	}
	if *localPort == 0 {
		*localPort = defaultPort
	}

	logf, logw, closeLog := newLogger(verbose, logPath("mcp"))
	defer closeLog()
	ctx, cancel := signalContext()
	defer cancel()
	instructions := buildInstructions(*name, *label, *desc)
	keyPath, _ := config.ClientKeyPath()
	cl, err := tunnel.NewClient(addr, keyPath, logf)
	if err != nil {
		return codeTunnel, err
	}
	defer cl.Close()

	if *backend == "safari" {
		return runMCPSafari(ctx, cl, uint16(*remotePort), instructions, logf)
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *localPort))
	if err != nil {
		logf("port %d busy (%v), picking a free one", *localPort, err)
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return codeGeneric, err
		}
	}
	defer ln.Close()
	actualPort := ln.Addr().(*net.TCPAddr).Port
	go cl.Forward(ctx, ln, uint16(*remotePort), func(c net.Conn, derr error) {
		logf("remote dial failed: %v", derr)
		write503(c, derr)
	})
	cmdName, cmdArgs, err := mcp.BackendCommand(*backend, actualPort, *mcpVersion)
	if err != nil {
		logf("%v", err)
		return codeNoNode, err
	}
	logf("mcp %s: %s %s (local %d → remote %d, %s)", *name, cmdName, strings.Join(cmdArgs, " "), actualPort, *remotePort, addr[:min(len(addr), 12)]+"…")
	stderr := io.MultiWriter(logw, os.Stderr)
	code, err := mcp.RunChild(ctx, cmdName, cmdArgs, instructions, stderr)
	logf("mcp %s exited with %d", *name, code)
	return code, err
}

func runMCPSafari(ctx context.Context, cl *tunnel.Client, remotePort uint16, instructions string, logf tunnel.Logf) (int, error) {
	br := bufio.NewReaderSize(os.Stdin, 1<<20)
	first, err := br.ReadBytes('\n')
	if err != nil && len(first) == 0 {
		return codeOK, nil // client went away before saying anything
	}
	conn, derr := cl.Dial(ctx, remotePort)
	if derr != nil {
		logf("remote dial failed: %v", derr)
		var m struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(first, &m)
		id := string(m.ID)
		if id == "" {
			id = "null"
		}
		msg, _ := json.Marshal(remoteDownMessage(derr))
		fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":%s}}`+"\n", id, msg)
		return codeTunnel, nil
	}
	defer conn.Close()
	stdin := io.MultiReader(bytes.NewReader(first), br)
	if err := mcp.Proxy(stdin, conn, conn, os.Stdout, instructions); err != nil {
		logf("safari proxy ended: %v", err)
	}
	return codeOK, nil
}

func remoteDownMessage(err error) string {
	return "远程主机的 rchrome serve 未运行，或地址已失效。请在远程主机执行 rchrome serve 后重试。 / Remote rchrome serve is not running or the address is stale: " + err.Error()
}

func write503(c net.Conn, err error) {
	defer c.Close()
	msg := remoteDownMessage(err)
	_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	// Puppeteer / Playwright surface only the status line's reason phrase, so
	// the explanation goes there (ASCII only) as well as in the body.
	fmt.Fprintf(c, "HTTP/1.1 503 remote rchrome serve is not running - run 'rchrome serve' on the remote machine\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(msg), msg)
}

// ---------------------------------------------------------------- forward

func runForward(args []string, verbose bool) (int, error) {
	fs := flag.NewFlagSet("forward", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "用法: rchrome forward [tc地址] [flags]\n纯端口转发（前台运行）：把远程调试端口映射到本机，给 Playwright 脚本等直接 connectOverCDP 用。\n")
		fs.PrintDefaults()
	}
	localPort := fs.Int("local-port", defaultPort, "本机监听端口")
	bind := fs.String("bind", "127.0.0.1", "本机监听地址；只有别的机器也要访问时才用 0.0.0.0")
	remotePort := fs.Int("remote-port", defaultPort, "远程端口")
	fs.BoolVar(&verbose, "verbose", verbose, "隧道日志输出到 stderr")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return codeGeneric, err
	}
	addr := ""
	if len(pos) > 0 {
		addr = strings.TrimSpace(pos[0])
	} else if saved, err := config.LoadConnect(); err == nil {
		addr = saved.Addr
	}
	if addr == "" {
		return codeGeneric, config.ErrNoConnect
	}
	logf, _, closeLog := newLogger(verbose, logPath("forward"))
	defer closeLog()
	ctx, cancel := signalContext()
	defer cancel()
	keyPath, _ := config.ClientKeyPath()
	cl, err := tunnel.NewClient(addr, keyPath, logf)
	if err != nil {
		return codeTunnel, err
	}
	defer cl.Close()
	ln, err := net.Listen("tcp", net.JoinHostPort(*bind, strconv.Itoa(*localPort)))
	if err != nil {
		return codeGeneric, err
	}
	defer ln.Close()
	go cl.Forward(ctx, ln, uint16(*remotePort), func(c net.Conn, derr error) { write503(c, derr) })
	fmt.Printf("✓ 本机 %s  →  远程 %d   （Playwright: connectOverCDP('http://127.0.0.1:%d')）\nCtrl+C 结束\n", ln.Addr(), *remotePort, *localPort)
	<-ctx.Done()
	fmt.Println()
	return codeOK, nil
}

// ---------------------------------------------------------------- list / doctor / keygen

func runList(args []string) (int, error) {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "机器可读输出")
	if _, err := parseInterspersed(fs, args); err != nil {
		return codeGeneric, err
	}
	list, err := config.ListConnections()
	if err != nil {
		return codeGeneric, err
	}
	if *jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(list)
		return codeOK, nil
	}
	if len(list) == 0 {
		fmt.Println("还没有连接过任何远程主机。先在远程运行 rchrome serve，再在本机 rchrome connect <地址>。")
		return codeOK, nil
	}
	fmt.Printf("%-18s %-20s %-10s %-9s %-24s %s\n", "名称(工具前缀)", "备注", "后端", "系统", "浏览器", "地址")
	for _, c := range list {
		short := c.Addr
		if len(short) > 16 {
			short = short[:16] + "…"
		}
		fmt.Printf("%-18s %-20s %-10s %-9s %-24s %s\n", c.Name, c.Label, c.Backend, c.OS, c.Browser, short)
	}
	return codeOK, nil
}

func runDoctor(args []string, verbose bool) (int, error) {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.BoolVar(&verbose, "verbose", verbose, "")
	if _, err := parseInterspersed(fs, args); err != nil {
		return codeGeneric, err
	}
	ok := func(b bool) string {
		if b {
			return "✓"
		}
		return "✗"
	}
	fmt.Printf("rchrome %s  %s/%s\n", version, runtime.GOOS, runtime.GOARCH)
	if p, err := chrome.Find(); err == nil {
		fmt.Printf("%s Chrome: %s\n", ok(true), p)
	} else {
		fmt.Printf("%s Chrome: 没找到（远程主机侧才需要；可用 serve --chrome 指定）\n", ok(false))
	}
	if runtime.GOOS == "darwin" {
		if sv, err := checkSafaridriver(); err == nil {
			fmt.Printf("%s safaridriver: %s\n", ok(true), sv)
		} else {
			fmt.Printf("%s safaridriver: %v\n", ok(false), err)
		}
	}
	if p, err := mcp.NpxPath(); err == nil {
		v, _ := exec.Command("node", "--version").Output()
		fmt.Printf("%s Node/npx: %s %s\n", ok(true), p, strings.TrimSpace(string(v)))
	} else {
		fmt.Printf("%s Node/npx: 没找到（本机侧需要，用于 chrome-devtools-mcp）\n", ok(false))
	}
	if p, err := mcp.ClaudePath(); err == nil {
		fmt.Printf("%s Claude Code CLI: %s\n", ok(true), p)
	} else {
		fmt.Printf("%s Claude Code CLI: 没找到（connect 会改为打印手动配置）\n", ok(false))
	}
	if d, err := config.Dir(); err == nil {
		fmt.Printf("%s 配置目录: %s\n", ok(true), d)
	} else {
		fmt.Printf("%s 配置目录: %v\n", ok(false), err)
	}
	list, _ := config.ListConnections()
	fmt.Printf("%s 已保存的远程主机: %d 个\n", ok(len(list) > 0), len(list))
	logf, _, closeLog := newLogger(verbose, "")
	defer closeLog()
	for _, c := range list {
		keyPath, _ := config.ClientKeyPath()
		cl, err := tunnel.NewClient(c.Addr, keyPath, logf)
		if err != nil {
			fmt.Printf("  %s %s: %v\n", ok(false), c.Name, err)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		lat, err := cl.Ping(ctx)
		cancel()
		_ = cl.Close()
		if err != nil {
			fmt.Printf("  %s %s（%s）: 连不上 — 远程 rchrome serve 没在跑？(%v)\n", ok(false), c.Name, c.Label, err)
		} else {
			fmt.Printf("  %s %s（%s）: 可达，中继往返 %s\n", ok(true), c.Name, c.Label, lat.Round(time.Millisecond))
		}
	}
	return codeOK, nil
}

func runKeygen(args []string) (int, error) {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	if _, err := parseInterspersed(fs, args); err != nil {
		return codeGeneric, err
	}
	keyPath, err := config.ClientKeyPath()
	if err != nil {
		return codeGeneric, err
	}
	k, err := tunnel.LoadOrCreateClientKey(keyPath)
	if err != nil {
		return codeGeneric, err
	}
	fmt.Printf("本机身份（给远程 serve --allow 用）:\n%s\n", k.Public().String())
	return codeOK, nil
}
