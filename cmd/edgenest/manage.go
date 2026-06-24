// Operator-facing CLI: the `edgenest` management menu and its subcommands.
//
// Pain it solves: after install, the panel URL is http://<ip>:<port>/<random
// path>. If the operator didn't bookmark it, that random path is unrecoverable
// from memory, and the install banner's commands are long. Re-running a short
// `edgenest` (the binary is already on PATH at /usr/local/bin) prints the URL,
// account, and service state — and, x-ui style, offers restart/logs/reset-pass/
// uninstall from one menu.
//
// All of this is read-mostly glue over the existing DB + systemd; it never
// touches the proxy data path (render / outbounds / route), so it cannot affect
// any inbound's behaviour.
package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/aipo-lenshow/EdgeNest/internal/control/auth"
	"github.com/aipo-lenshow/EdgeNest/internal/control/bootstrap"
	"github.com/aipo-lenshow/EdgeNest/internal/control/config"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

const systemdUnitPath = "/etc/systemd/system/edgenest.service"

// dispatchManage handles operator CLI subcommands and the bare-invocation menu.
// It returns true when it fully handled the invocation, so main() returns without
// starting the server. It runs BEFORE flag parsing on purpose: the systemd unit
// always launches with --role/--listen flags, so it can never be mistaken for a
// management command, and a developer running the binary bare as non-root still
// gets the server.
func dispatchManage(args []string) bool {
	bare := len(args) == 0
	sub := len(args) >= 1 && !strings.HasPrefix(args[0], "-")

	// Operator invocations (the bare menu or an explicit subcommand) need root:
	// the DB lives in /etc/edgenest (mode 0750, root-owned) and the actions drive
	// systemctl. Most cloud images log the operator in as a non-root sudo user
	// (ubuntu / ec2-user / debian), so a bare `edgenest` there must NOT fall
	// through and start a rogue foreground server as that user (which can't write
	// /etc/edgenest and confuses the operator). When a system install is present,
	// re-exec the same invocation under sudo so the menu / subcommand just works.
	// A dev box has no systemd unit, so this never fires for `go run`.
	if (bare || sub) && isInteractive() && os.Geteuid() != 0 && systemInstallPresent() {
		return reexecWithSudo(args)
	}

	if sub {
		setColors()
		switch args[0] {
		case "status", "info":
			runStatus()
		case "menu":
			runMenu()
		case "reset-pass", "resetpass", "passwd":
			runResetPass()
		case "uninstall", "remove":
			runUninstall()
		default:
			fmt.Fprintf(os.Stderr, "edgenest: unknown command %q\n", args[0])
			fmt.Fprintln(os.Stderr, "commands: status | menu | reset-pass | uninstall   (run with no args on a terminal to open the menu)")
			os.Exit(2)
		}
		return true
	}
	// Bare invocation, interactive root shell, existing install → open the menu.
	// Guarded triple so it never fires for the systemd service (has flags),
	// a piped/non-interactive call, or a dev `go run` as non-root.
	if bare && isInteractive() && os.Geteuid() == 0 {
		cfg := config.Default()
		if _, err := os.Stat(cfg.DBPath()); err == nil {
			setColors()
			runMenu()
			return true
		}
	}
	return false
}

// systemInstallPresent reports whether EdgeNest is installed as a system service.
// The systemd unit is world-readable, so this works regardless of euid — unlike
// config.Default(), whose data dir is HOME-relative for a non-root caller and so
// can't tell "operator forgot sudo" apart from "dev box, no install".
func systemInstallPresent() bool {
	_, err := os.Stat(systemdUnitPath)
	return err == nil
}

// reexecWithSudo re-runs this exact invocation under sudo so a non-root operator
// gets the management menu / subcommand against the root-owned install. It always
// reports the invocation as handled so main() never falls through to the server.
// If sudo is unavailable it prints a hint and exits non-zero.
func reexecWithSudo(args []string) bool {
	self, err := os.Executable()
	if err != nil || self == "" {
		self = "/usr/local/bin/edgenest"
	}
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		fmt.Fprintln(os.Stderr, "edgenest: management commands need root — re-run as: sudo edgenest "+strings.Join(args, " "))
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "edgenest: re-running with sudo for management access…")
	cmd := exec.Command(sudo, append([]string{self}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		os.Exit(1) // sudo auth declined / cancelled — surface non-zero.
	}
	return true
}

func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// ---- data helpers ----

func openManageStore() (*store.Store, config.Config) {
	cfg := config.Default()
	if _, err := os.Stat(cfg.DBPath()); err != nil {
		fmt.Fprintf(os.Stderr, "edgenest: no install found at %s — run the installer first.\n", cfg.DataDir)
		os.Exit(1)
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "edgenest: open db: %v\n", err)
		os.Exit(1)
	}
	return st, cfg
}

// resolveListen returns the --listen value the service actually runs with, parsed
// from the systemd unit's ExecStart (install.sh may have chosen a custom port or
// family). Falls back to the config default when no unit is present.
func resolveListen(def string) string {
	b, err := os.ReadFile(systemdUnitPath)
	if err != nil {
		return def
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ExecStart=") {
			continue
		}
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "--listen" && i+1 < len(fields) {
				return fields[i+1]
			}
			if strings.HasPrefix(f, "--listen=") {
				return strings.TrimPrefix(f, "--listen=")
			}
		}
	}
	return def
}

func serviceState() string {
	out, _ := exec.Command("systemctl", "is-active", "edgenest").Output()
	s := strings.TrimSpace(string(out))
	if s == "" {
		return "unknown"
	}
	return s
}

// panelURLs assembles every browser-reachable panel URL. A wildcard bind (the
// normal case) expands to the operator's share_host plus each probed v4/v6
// address; net.JoinHostPort brackets v6 literals. A concrete bind host is
// returned verbatim.
func panelURLs(listen, shareHost, panelPath string, cap core.NodeCapability) []string {
	host, port := splitHostPort(listen)
	path := "/" + strings.TrimPrefix(panelPath, "/")
	mk := func(h string) string { return "http://" + net.JoinHostPort(h, port) + path }
	if !isWildcard(host) {
		return []string{mk(host)}
	}
	seen := map[string]bool{}
	var urls []string
	add := func(h string) {
		if h == "" || seen[h] {
			return
		}
		seen[h] = true
		urls = append(urls, mk(h))
	}
	add(shareHost) // operator's canonical address / domain first
	for _, a := range cap.IPv4Addrs {
		add(a)
	}
	if len(cap.IPv4Addrs) == 0 {
		add(cap.IPv4Addr)
	}
	for _, a := range cap.IPv6Addrs {
		add(a)
	}
	if len(cap.IPv6Addrs) == 0 {
		add(cap.IPv6Addr)
	}
	if len(urls) == 0 {
		urls = append(urls, mk("<your-server-ip>"))
	}
	return urls
}

func splitHostPort(listen string) (host, port string) {
	if strings.HasPrefix(listen, ":") {
		return "", strings.TrimPrefix(listen, ":")
	}
	h, p, err := net.SplitHostPort(listen)
	if err != nil {
		return listen, "2087"
	}
	return h, p
}

func isWildcard(h string) bool {
	switch h {
	case "", "0.0.0.0", "::", "[::]":
		return true
	}
	return false
}

func menuLang(st *store.Store) string {
	// default_lang holds the full code the installer/panel chose (en | zh |
	// zh-TW | fa | ru | vi). Return it verbatim when the menu has that language
	// so the CLI matches install.sh / uninstall.sh / the panel; only an unset or
	// unknown value falls back (to zh, the historical default).
	v, _ := st.GetSetting(bootstrap.KeyDefaultLang)
	if _, ok := manageStr[v]; ok {
		return v
	}
	return "zh"
}

// ---- subcommands ----

func runStatus() {
	st, cfg := openManageStore()
	renderStatus(st, cfg, menuLang(st))
}

func renderStatus(st *store.Store, cfg config.Config, lang string) {
	panelPath, _ := st.GetSetting(bootstrap.KeyPanelPath)
	shareHost, _ := st.GetSetting("share_host")
	cap := core.ReadNodeCapability(core.DefaultCapabilityPath)
	urls := panelURLs(resolveListen(cfg.Listen), shareHost, panelPath, cap)
	username := "?"
	if a, err := st.FirstAdmin(); err == nil {
		username = a.Username
	}

	state := serviceState()
	dot := cGreen + "●" + cReset
	if state != "active" {
		dot = cRed + "●" + cReset
	}
	fmt.Printf("\n%sEdgeNest %s%s   %s %s\n", cBold, version, cReset, dot, state)
	fmt.Printf("  %s\n", tr(lang, "f_panel"))
	for _, u := range urls {
		fmt.Printf("    %s%s%s\n", cGreen, u, cReset)
	}
	fmt.Printf("  %-10s %s%s%s\n", tr(lang, "f_login"), cYellow, username, cReset)
	fmt.Printf("  %-10s %s\n", tr(lang, "f_data"), cfg.DataDir)
	fmt.Printf("  %-10s %ssudo journalctl -u edgenest -f%s\n\n", tr(lang, "f_logs"), cDim, cReset)
}

func runResetPass() {
	st, _ := openManageStore()
	lang := menuLang(st)
	r := bufio.NewReader(os.Stdin)
	if !confirm(tr(lang, "rp_confirm"), r) {
		fmt.Println(tr(lang, "cancelled"))
		return
	}
	renderResetPass(st, lang)
}

func renderResetPass(st *store.Store, lang string) {
	a, err := st.FirstAdmin()
	if err != nil {
		fmt.Println(tr(lang, "rp_noadmin"))
		return
	}
	pw, err := auth.RandomHex(8) // 16-char hex, same strength as first-run
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate password: %v\n", err)
		return
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hash password: %v\n", err)
		return
	}
	a.PasswordHash = hash
	a.MustChangePassword = true
	if err := st.UpdateAdmin(a); err != nil {
		fmt.Fprintf(os.Stderr, "save admin: %v\n", err)
		return
	}
	fmt.Printf("\n%s%s%s\n", cGreen, tr(lang, "rp_done"), cReset)
	fmt.Printf("  %-10s %s\n", tr(lang, "f_login"), a.Username)
	fmt.Printf("  %-10s %s%s%s\n", tr(lang, "rp_newpw"), cYellow, pw, cReset)
	fmt.Printf("  %s%s%s\n\n", cDim, tr(lang, "rp_note"), cReset)
}

func runUninstall() {
	cfg := config.Default()
	runUninstallScript(cfg)
}

// runUninstallScript hands off to the installer-deployed uninstall.sh (single
// source of truth: it knows about volumes, xray share dir, the systemd unit,
// and asks its own keep-data / confirm questions). install.sh copies it into
// the data dir so it survives even if the source tree is gone.
func runUninstallScript(cfg config.Config) {
	script := filepath.Join(cfg.DataDir, "uninstall.sh")
	if _, err := os.Stat(script); err != nil {
		fmt.Printf("uninstall script not found at %s\n", script)
		fmt.Println("run the uninstaller from the source tree: sudo bash scripts/uninstall.sh")
		return
	}
	cmd := exec.Command("bash", script)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	_ = cmd.Run()
}

// ---- interactive menu ----

func runMenu() {
	st, cfg := openManageStore()
	lang := menuLang(st)
	r := bufio.NewReader(os.Stdin)
	for {
		state := serviceState()
		dot := cGreen + "●" + cReset
		if state != "active" {
			dot = cRed + "●" + cReset
		}
		fmt.Printf("\n%sEdgeNest %s%s  %s %s\n", cBold, version, cReset, dot, state)
		fmt.Printf(" 1) %s\n", tr(lang, "m_status"))
		fmt.Printf(" 2) %s\n", tr(lang, "m_svc"))
		fmt.Printf(" 3) %s\n", tr(lang, "m_logs"))
		fmt.Printf(" 4) %s\n", tr(lang, "m_resetpass"))
		fmt.Printf(" 5) %s\n", tr(lang, "m_uninstall"))
		fmt.Printf(" 0) %s\n", tr(lang, "m_exit"))
		fmt.Print(tr(lang, "prompt"))
		line, _ := r.ReadString('\n')
		switch strings.TrimSpace(line) {
		case "1":
			renderStatus(st, cfg, lang)
			pause(lang, r)
		case "2":
			svcSubmenu(lang, r)
		case "3":
			tailLogs(lang)
		case "4":
			if confirm(tr(lang, "rp_confirm"), r) {
				renderResetPass(st, lang)
			}
			pause(lang, r)
		case "5":
			runUninstallScript(cfg)
			// If uninstall ran, the binary itself may be gone next loop; either
			// way returning to the menu after a removal attempt is fine.
			return
		case "0", "q", "":
			return
		default:
			fmt.Println(tr(lang, "badchoice"))
		}
	}
}

func svcSubmenu(lang string, r *bufio.Reader) {
	fmt.Printf("   1) %s   2) %s   3) %s   0) %s\n", tr(lang, "svc_restart"), tr(lang, "svc_stop"), tr(lang, "svc_start"), tr(lang, "m_back"))
	fmt.Print(tr(lang, "prompt"))
	line, _ := r.ReadString('\n')
	var verb string
	switch strings.TrimSpace(line) {
	case "1":
		verb = "restart"
	case "2":
		verb = "stop"
	case "3":
		verb = "start"
	default:
		return
	}
	out, err := exec.Command("systemctl", verb, "edgenest").CombinedOutput()
	if err != nil {
		fmt.Printf("%s%s%s\n%s\n", cRed, tr(lang, "svc_fail"), cReset, strings.TrimSpace(string(out)))
		return
	}
	fmt.Printf("%s%s %s%s\n", cGreen, verb, tr(lang, "svc_ok"), cReset)
}

// tailLogs streams the journal live. Ctrl-C is swallowed by this process so it
// only terminates the journalctl child and drops back to the menu, instead of
// killing the whole program (the two share the foreground process group).
func tailLogs(lang string) {
	fmt.Printf("%s%s%s\n", cDim, tr(lang, "logs_hint"), cReset)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-sig:
			case <-done:
				return
			}
		}
	}()
	cmd := exec.Command("journalctl", "-u", "edgenest", "-n", "80", "-f")
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	_ = cmd.Run()
	close(done)
	signal.Stop(sig)
}

func confirm(prompt string, r *bufio.Reader) bool {
	fmt.Print(prompt)
	line, _ := r.ReadString('\n')
	s := strings.ToLower(strings.TrimSpace(line))
	return s == "y" || s == "yes"
}

func pause(lang string, r *bufio.Reader) {
	fmt.Print(tr(lang, "pause"))
	_, _ = r.ReadString('\n')
}

// ---- colors ----

var (
	cReset, cBold, cGreen, cYellow, cCyan, cDim, cRed string
)

// setColors enables ANSI styling only when stdout is a terminal, mirroring
// install.sh / uninstall.sh behaviour (no escape codes in piped output / logs).
func setColors() {
	fi, err := os.Stdout.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return
	}
	cReset, cBold, cGreen, cYellow, cCyan, cDim, cRed =
		"\033[0m", "\033[1m", "\033[32m", "\033[33m", "\033[36m", "\033[2m", "\033[31m"
}

// ---- tiny bilingual table (server CLI surface; the web UI keeps its own i18n) ----

func tr(lang, key string) string {
	if m, ok := manageStr[lang]; ok {
		if v, ok := m[key]; ok {
			return v
		}
	}
	return manageStr["en"][key]
}

var manageStr = map[string]map[string]string{
	"zh": {
		"f_panel":     "面板地址",
		"f_login":     "账号",
		"f_data":      "数据目录",
		"f_logs":      "日志",
		"m_status":    "查看面板地址 / 账号",
		"m_svc":       "重启 / 停止 / 启动",
		"m_logs":      "实时日志",
		"m_resetpass": "重置管理员密码",
		"m_uninstall": "卸载",
		"m_exit":      "退出",
		"m_back":      "返回",
		"prompt":      "请选择: ",
		"pause":       "按回车返回菜单… ",
		"badchoice":   "无效选项。",
		"cancelled":   "已取消。",
		"svc_restart": "重启",
		"svc_stop":    "停止",
		"svc_start":   "启动",
		"svc_ok":      "完成。",
		"svc_fail":    "操作失败:",
		"logs_hint":   "实时日志 — 按 Ctrl-C 返回菜单",
		"rp_confirm":  "确认重置管理员密码? 旧密码立即失效 [y/N]: ",
		"rp_done":     "管理员密码已重置 — 请记下新密码 (只显示这一次):",
		"rp_newpw":    "新密码",
		"rp_note":     "下次登录后会要求你再改一次密码。",
		"rp_noadmin":  "未找到管理员账号 (尚未初始化?)。",
	},
	"en": {
		"f_panel":     "Panel",
		"f_login":     "Login",
		"f_data":      "Data dir",
		"f_logs":      "Logs",
		"m_status":    "Show panel URL / account",
		"m_svc":       "Restart / stop / start",
		"m_logs":      "Live logs",
		"m_resetpass": "Reset admin password",
		"m_uninstall": "Uninstall",
		"m_exit":      "Exit",
		"m_back":      "Back",
		"prompt":      "Choose: ",
		"pause":       "Press Enter to return to the menu… ",
		"badchoice":   "Invalid choice.",
		"cancelled":   "Cancelled.",
		"svc_restart": "restart",
		"svc_stop":    "stop",
		"svc_start":   "start",
		"svc_ok":      "done.",
		"svc_fail":    "action failed:",
		"logs_hint":   "Live logs — press Ctrl-C to return to the menu",
		"rp_confirm":  "Reset the admin password? The old one stops working immediately [y/N]: ",
		"rp_done":     "Admin password reset — write down the new one (shown once):",
		"rp_newpw":    "New password",
		"rp_note":     "You'll be asked to change it again after your next login.",
		"rp_noadmin":  "No admin account found (not initialised yet?).",
	},
	"zh-TW": {
		"f_panel":     "面板地址",
		"f_login":     "帳號",
		"f_data":      "資料目錄",
		"f_logs":      "日誌",
		"m_status":    "檢視面板地址 / 帳號",
		"m_svc":       "重啟 / 停止 / 啟動",
		"m_logs":      "即時日誌",
		"m_resetpass": "重設管理員密碼",
		"m_uninstall": "解除安裝",
		"m_exit":      "結束",
		"m_back":      "返回",
		"prompt":      "請選擇: ",
		"pause":       "按 Enter 返回選單… ",
		"badchoice":   "無效選項。",
		"cancelled":   "已取消。",
		"svc_restart": "重啟",
		"svc_stop":    "停止",
		"svc_start":   "啟動",
		"svc_ok":      "完成。",
		"svc_fail":    "操作失敗:",
		"logs_hint":   "即時日誌 — 按 Ctrl-C 返回選單",
		"rp_confirm":  "確認重設管理員密碼? 舊密碼立即失效 [y/N]: ",
		"rp_done":     "管理員密碼已重設 — 請記下新密碼 (只顯示這一次):",
		"rp_newpw":    "新密碼",
		"rp_note":     "下次登入後會要求你再改一次密碼。",
		"rp_noadmin":  "找不到管理員帳號 (尚未初始化?)。",
	},
	"fa": {
		"f_panel":     "آدرس پنل",
		"f_login":     "حساب",
		"f_data":      "پوشهٔ داده",
		"f_logs":      "لاگ‌ها",
		"m_status":    "نمایش آدرس پنل / حساب",
		"m_svc":       "راه‌اندازی مجدد / توقف / شروع",
		"m_logs":      "لاگ‌های زنده",
		"m_resetpass": "بازنشانی رمز مدیر",
		"m_uninstall": "حذف نصب",
		"m_exit":      "خروج",
		"m_back":      "بازگشت",
		"prompt":      "انتخاب کنید: ",
		"pause":       "برای بازگشت به منو Enter را بزنید… ",
		"badchoice":   "گزینهٔ نامعتبر.",
		"cancelled":   "لغو شد.",
		"svc_restart": "راه‌اندازی مجدد",
		"svc_stop":    "توقف",
		"svc_start":   "شروع",
		"svc_ok":      "انجام شد.",
		"svc_fail":    "عملیات ناموفق بود:",
		"logs_hint":   "لاگ‌های زنده — برای بازگشت به منو Ctrl-C را بزنید",
		"rp_confirm":  "رمز مدیر بازنشانی شود؟ رمز قبلی بلافاصله از کار می‌افتد [y/N]: ",
		"rp_done":     "رمز مدیر بازنشانی شد — رمز جدید را یادداشت کنید (فقط یک‌بار نمایش داده می‌شود):",
		"rp_newpw":    "رمز جدید",
		"rp_note":     "پس از ورود بعدی از شما خواسته می‌شود دوباره رمز را تغییر دهید.",
		"rp_noadmin":  "حساب مدیر پیدا نشد (هنوز مقداردهی اولیه نشده؟).",
	},
	"ru": {
		"f_panel":     "Панель",
		"f_login":     "Логин",
		"f_data":      "Каталог данных",
		"f_logs":      "Логи",
		"m_status":    "Показать URL панели / аккаунт",
		"m_svc":       "Перезапуск / остановка / запуск",
		"m_logs":      "Логи в реальном времени",
		"m_resetpass": "Сбросить пароль администратора",
		"m_uninstall": "Удалить",
		"m_exit":      "Выход",
		"m_back":      "Назад",
		"prompt":      "Выбор: ",
		"pause":       "Нажмите Enter, чтобы вернуться в меню… ",
		"badchoice":   "Неверный выбор.",
		"cancelled":   "Отменено.",
		"svc_restart": "перезапуск",
		"svc_stop":    "остановка",
		"svc_start":   "запуск",
		"svc_ok":      "готово.",
		"svc_fail":    "ошибка операции:",
		"logs_hint":   "Логи в реальном времени — нажмите Ctrl-C для возврата в меню",
		"rp_confirm":  "Сбросить пароль администратора? Старый сразу перестанет работать [y/N]: ",
		"rp_done":     "Пароль администратора сброшен — запишите новый (показывается один раз):",
		"rp_newpw":    "Новый пароль",
		"rp_note":     "При следующем входе вам снова предложат сменить пароль.",
		"rp_noadmin":  "Аккаунт администратора не найден (ещё не инициализирован?).",
	},
	"vi": {
		"f_panel":     "Bảng điều khiển",
		"f_login":     "Tài khoản",
		"f_data":      "Thư mục dữ liệu",
		"f_logs":      "Nhật ký",
		"m_status":    "Hiển thị URL bảng điều khiển / tài khoản",
		"m_svc":       "Khởi động lại / dừng / khởi động",
		"m_logs":      "Nhật ký trực tiếp",
		"m_resetpass": "Đặt lại mật khẩu quản trị",
		"m_uninstall": "Gỡ cài đặt",
		"m_exit":      "Thoát",
		"m_back":      "Quay lại",
		"prompt":      "Chọn: ",
		"pause":       "Nhấn Enter để quay lại menu… ",
		"badchoice":   "Lựa chọn không hợp lệ.",
		"cancelled":   "Đã hủy.",
		"svc_restart": "khởi động lại",
		"svc_stop":    "dừng",
		"svc_start":   "khởi động",
		"svc_ok":      "xong.",
		"svc_fail":    "thao tác thất bại:",
		"logs_hint":   "Nhật ký trực tiếp — nhấn Ctrl-C để quay lại menu",
		"rp_confirm":  "Đặt lại mật khẩu quản trị? Mật khẩu cũ ngừng hoạt động ngay [y/N]: ",
		"rp_done":     "Đã đặt lại mật khẩu quản trị — ghi lại mật khẩu mới (chỉ hiển thị một lần):",
		"rp_newpw":    "Mật khẩu mới",
		"rp_note":     "Bạn sẽ được yêu cầu đổi lại mật khẩu sau lần đăng nhập tiếp theo.",
		"rp_noadmin":  "Không tìm thấy tài khoản quản trị (chưa khởi tạo?).",
	},
}
