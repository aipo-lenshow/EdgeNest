// Package botrunner runs the interactive Telegram management bot. It long-polls
// getUpdates (no public webhook / open port needed — self-host friendly), routes
// slash commands, and enforces an admin chat-ID allowlist so only the operator
// can query or control the panel. The daily-summary push lives in notifyrunner;
// this package shares the same bot token but is the sole getUpdates consumer.
//
// Scope: read-only commands. Write commands (disable/quota/expire/create/
// delete) and proactive alerts land in later phases.
package botrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/aipo-lenshow/EdgeNest/internal/control/digest"
	"github.com/aipo-lenshow/EdgeNest/internal/control/notify"
	"github.com/aipo-lenshow/EdgeNest/internal/control/orchestrator"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/control/system"
	"github.com/aipo-lenshow/EdgeNest/internal/control/updatecheck"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
	"github.com/aipo-lenshow/EdgeNest/internal/core/nodeapi"
)

// Setting keys. The token is shared with the daily-summary runner; the bot adds
// its own enable flag, admin allowlist, and a durable getUpdates offset.
const (
	keyBotEnabled   = "bot_enabled"             // "true"/"false"
	keyToken        = "notify_telegram_token"   // shared with notifyrunner
	keyAdminChatIDs = "bot_admin_chat_ids"      // JSON []string allowlist
	keyFallbackChat = "notify_telegram_chat_id" // seed allowlist when unset
	keyOffset       = "bot_update_offset"       // last processed update_id
	keyShareHost    = "share_host"
	keyDisplayTZ    = "display_tz" // IANA; empty = server local
)

// longPollSec is the getUpdates server-side wait. Kept at 30s so toggling
// bot_enabled off is noticed within ~one window.
const longPollSec = 30

type Runner struct {
	store       *store.Store
	node        nodeapi.NodeClient
	orch        *orchestrator.Orchestrator
	nodeID      string
	panelPort   int
	offset      int64
	lastCmdLang string // last language the command menu was registered for

	mu   sync.Mutex
	pend map[int64]*pendingAction // chatID → awaiting-confirm destructive op
	sess map[int64]*session       // chatID → in-progress selection flow (P7)
}

func New(s *store.Store, n nodeapi.NodeClient, orch *orchestrator.Orchestrator, nodeID string, panelPort int) *Runner {
	return &Runner{
		store: s, node: n, orch: orch, nodeID: nodeID, panelPort: panelPort,
		pend: map[int64]*pendingAction{},
		sess: map[int64]*session{},
	}
}

func (r *Runner) Start(ctx context.Context) {
	if v, _ := r.store.GetSetting(keyOffset); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			r.offset = n
		}
	}
	go r.loop(ctx)
}

func (r *Runner) loop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		if enabled, _ := r.store.GetSetting(keyBotEnabled); enabled != "true" {
			if sleep(ctx, 5*time.Second) {
				return
			}
			continue
		}
		token, _ := r.store.GetSetting(keyToken)
		if token == "" {
			if sleep(ctx, 5*time.Second) {
				return
			}
			continue
		}
		// Register / refresh the native command menu (☰ Menu + "/" autocomplete)
		// once per language so users can tap commands instead of typing them.
		if lang := r.lang(); lang != r.lastCmdLang {
			if err := notify.SetMyCommands(ctx, token, botCommands(lang)); err == nil {
				r.lastCmdLang = lang
			}
		}
		updates, maxID, err := notify.GetUpdates(ctx, token, r.offset, longPollSec)
		if err != nil {
			if sleep(ctx, 3*time.Second) {
				return
			}
			continue
		}
		allow := r.allowlist()
		for _, u := range updates {
			// Unknown chat → silently ignore (don't even reveal the bot exists).
			if !allow[strconv.FormatInt(u.ChatID, 10)] {
				if u.CallbackQ != "" {
					_ = notify.AnswerCallback(ctx, token, u.CallbackQ, "")
				}
				continue
			}
			switch {
			case u.Callback != "":
				r.handleCallback(ctx, token, u.ChatID, u.MessageID, u.CallbackQ, u.Callback)
			case u.Text != "":
				r.handleCommand(ctx, token, u.ChatID, u.Text)
			}
		}
		if maxID >= r.offset {
			r.offset = maxID + 1
			_ = r.store.SetSetting(keyOffset, strconv.FormatInt(r.offset, 10))
		}
	}
}

// allowlist returns the set of chat IDs permitted to use the bot. Falls back to
// the daily-summary chat ID when no explicit allowlist is configured.
func (r *Runner) allowlist() map[string]bool {
	out := map[string]bool{}
	raw, _ := r.store.GetSetting(keyAdminChatIDs)
	if raw != "" {
		var ids []string
		if json.Unmarshal([]byte(raw), &ids) == nil {
			for _, id := range ids {
				if id = strings.TrimSpace(id); id != "" {
					out[id] = true
				}
			}
		}
	}
	if len(out) == 0 {
		if fb, _ := r.store.GetSetting(keyFallbackChat); fb != "" {
			out[strings.TrimSpace(fb)] = true
		}
	}
	return out
}

func (r *Runner) reply(ctx context.Context, token string, chatID int64, html string) {
	_ = notify.SendTelegramHTML(ctx, token, strconv.FormatInt(chatID, 10), html)
}

// replyPlain sends a plain-text message (no HTML parse mode). Used for the
// digest, which is built as plain text and may contain characters HTML mode
// would mis-parse.
func (r *Runner) replyPlain(ctx context.Context, token string, chatID int64, text string) {
	_ = notify.SendTelegram(ctx, token, strconv.FormatInt(chatID, 10), text)
}

// cmdSummary renders the same daily digest the scheduled push sends, on demand —
// the operator's one-tap "how is everything right now" command.
func (r *Runner) cmdSummary(ctx context.Context, lang string) string {
	text, err := digest.Build(ctx, r.store, r.node, r.nodeID, lang, r.location())
	if err != nil {
		return tr(lang, "⚠️ 无法生成概览: ", "⚠️ Could not build summary: ") + err.Error()
	}
	return text
}

// handleCommand parses "/cmd args…", maps it to a canonical command (accepting
// both English and localized aliases), and dispatches. Replies follow the
// panel's display language (default_lang). Read-only in P3.
func (r *Runner) handleCommand(ctx context.Context, token string, chatID int64, text string) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return
	}
	tok := strings.ToLower(fields[0])
	// Strip a trailing @botname (group chats append it). Lowercasing is safe for
	// the Chinese aliases too (they have no case).
	if i := strings.Index(tok, "@"); i >= 0 {
		tok = tok[:i]
	}
	arg := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
	lang := r.lang()

	switch canonicalCmd(tok, arg) {
	case "help":
		r.sendHelp(ctx, token, chatID, lang)
	case "summary":
		r.replyPlain(ctx, token, chatID, r.cmdSummary(ctx, lang))
	case "status":
		r.reply(ctx, token, chatID, r.cmdStatus(ctx, lang))
	case "users":
		r.reply(ctx, token, chatID, r.cmdUsers(lang))
	case "user":
		r.reply(ctx, token, chatID, r.cmdUser(lang, arg))
	case "top":
		r.reply(ctx, token, chatID, r.cmdTop(lang))
	case "traffic":
		r.reply(ctx, token, chatID, r.cmdTraffic(lang))
	case "node":
		r.reply(ctx, token, chatID, r.cmdNode(lang))
	case "sub":
		r.cmdSubReply(ctx, token, chatID, lang, arg)
	case "enable":
		r.reply(ctx, token, chatID, r.cmdSetEnabled(ctx, lang, arg, true))
	case "disable":
		r.reply(ctx, token, chatID, r.cmdSetEnabled(ctx, lang, arg, false))
	case "quota":
		r.reply(ctx, token, chatID, r.cmdQuota(ctx, lang, arg))
	case "expire":
		r.reply(ctx, token, chatID, r.cmdExpire(ctx, lang, arg))
	case "reset":
		r.reply(ctx, token, chatID, r.cmdReset(ctx, lang, arg))
	case "enforce":
		r.reply(ctx, token, chatID, r.cmdEnforce(ctx, lang))
	case "create":
		r.cmdCreatePrompt(ctx, token, chatID, lang, arg)
	case "delete":
		r.cmdDeletePrompt(ctx, token, chatID, lang, arg)
	default:
		// Unrecognized input → show the full tappable menu (not just plain text)
		// so the operator always has every command one tap away.
		r.sendHelp(ctx, token, chatID, lang)
	}
}

// canonicalCmd maps an English OR localized command token to its canonical id.
// "/用户" is dual: with an argument it's user-detail, bare it's the user list —
// natural since 用户 reads as both "user" and "users" in Chinese.
func canonicalCmd(tok, arg string) string {
	switch tok {
	case "/help", "/start", "/帮助", "/菜单":
		return "help"
	case "/summary", "/概览", "/汇总":
		return "summary"
	case "/status", "/状态":
		return "status"
	case "/users", "/用户列表":
		return "users"
	case "/user":
		return "user"
	case "/用户":
		if arg != "" {
			return "user"
		}
		return "users"
	case "/top", "/排行", "/排名":
		return "top"
	case "/traffic", "/流量":
		return "traffic"
	case "/node", "/节点":
		return "node"
	case "/sub", "/订阅":
		return "sub"
	case "/enable", "/启用":
		return "enable"
	case "/disable", "/禁用":
		return "disable"
	case "/quota", "/配额":
		return "quota"
	case "/expire", "/到期", "/期限":
		return "expire"
	case "/reset", "/重置":
		return "reset"
	case "/enforce", "/执行", "/强制":
		return "enforce"
	case "/create", "/创建", "/新建":
		return "create"
	case "/delete", "/删除":
		return "delete"
	}
	return ""
}

// tr picks the zh or en string for the current language.
func tr(lang, zh, en string) string {
	if lang == "zh" {
		return zh
	}
	return en
}

// botCommands is the registered command set powering "/" autocomplete. Names
// must be ASCII (the registered canonical names; localized aliases still work
// when typed). Unlike the native ☰ Menu sheet — whose visible height the TG
// client controls — the "/" autocomplete list shows every registered command,
// so we register the full set (read-only + management) here. The richer
// always-visible tappable menu is still the inline keyboard on /help.
func botCommands(lang string) []notify.BotCommand {
	return []notify.BotCommand{
		// read-only
		{Command: "summary", Description: tr(lang, "每日概览", "Daily digest")},
		{Command: "status", Description: tr(lang, "服务器状态", "Server status")},
		{Command: "users", Description: tr(lang, "用户列表", "User list")},
		{Command: "user", Description: tr(lang, "用户详情 <email>", "User detail <email>")},
		{Command: "top", Description: tr(lang, "流量排行", "Traffic ranking")},
		{Command: "traffic", Description: tr(lang, "服务器流量", "Server traffic")},
		{Command: "node", Description: tr(lang, "面板访问地址", "Panel access addresses")},
		{Command: "sub", Description: tr(lang, "订阅+二维码 <email>", "Subscription+QR <email>")},
		// management
		{Command: "enable", Description: tr(lang, "启用用户 <email>", "Enable user <email>")},
		{Command: "disable", Description: tr(lang, "禁用用户 <email>", "Disable user <email>")},
		{Command: "quota", Description: tr(lang, "设配额 <email> <大小>", "Set quota <email> <size>")},
		{Command: "expire", Description: tr(lang, "设到期 <email> <日期>", "Set expiry <email> <date>")},
		{Command: "reset", Description: tr(lang, "清空用量 <email>", "Reset usage <email>")},
		{Command: "create", Description: tr(lang, "新建用户(需确认)", "New user (confirm)")},
		{Command: "delete", Description: tr(lang, "删除用户(需确认)", "Delete user (confirm)")},
		{Command: "enforce", Description: tr(lang, "立即检查配额/到期", "Run quota/expiry check")},
		{Command: "help", Description: tr(lang, "命令菜单", "Command menu")},
	}
}

// menuButtons are the tappable inline buttons attached to /help. The query group
// runs immediately; the management group launches a guided selection flow (user
// picker → value buttons → confirm) so the operator never has to type an email —
// "select, don't fill". Only the user identifier on /create still allows typing.
func menuButtons(lang string) [][][2]string {
	return [][][2]string{
		// query (no-arg, immediate)
		{{tr(lang, "📋 概览", "📋 Summary"), "cmd:summary"}, {tr(lang, "📊 状态", "📊 Status"), "cmd:status"}},
		{{tr(lang, "👥 用户", "👥 Users"), "cmd:users"}, {tr(lang, "🏆 排行", "🏆 Top"), "cmd:top"}},
		{{tr(lang, "📈 流量", "📈 Traffic"), "cmd:traffic"}, {tr(lang, "🖥 节点", "🖥 Node"), "cmd:node"}},
		// management (selection flows)
		{{tr(lang, "✅ 启用", "✅ Enable"), "act:enable"}, {tr(lang, "🚫 禁用", "🚫 Disable"), "act:disable"}},
		{{tr(lang, "📦 配额", "📦 Quota"), "act:quota"}, {tr(lang, "⏰ 期限", "⏰ Expiry"), "act:expire"}},
		{{tr(lang, "🔄 重置", "🔄 Reset"), "act:reset"}, {tr(lang, "🔗 订阅", "🔗 Sub"), "act:sub"}},
		{{tr(lang, "➕ 创建", "➕ Create"), "act:create"}, {tr(lang, "🗑 删除", "🗑 Delete"), "act:delete"}},
		{{tr(lang, "⚡ 立即检查", "⚡ Run check"), "cmd:enforce"}, {tr(lang, "📖 命令语法", "📖 Syntax"), "cmd:cmdref"}},
	}
}

// sendHelp replies with the command list plus a tappable inline keyboard.
func (r *Runner) sendHelp(ctx context.Context, token string, chatID int64, lang string) {
	_ = notify.SendTelegramKeyboard(ctx, token, strconv.FormatInt(chatID, 10), r.helpText(lang), menuButtons(lang))
}

// handleCallback runs the command behind a tapped inline button. Buttons carry
// a namespaced callback: cmd:<name> (no-arg command), confirm:yes/no
// (destructive op), or one of the selection-flow prefixes (act/pick/pg/val/
// ib/nwq/nwe/nw) handled in interactive.go. messageID is the message the button
// is attached to, so selection flows can redraw it in place.
func (r *Runner) handleCallback(ctx context.Context, token string, chatID, messageID int64, cbID, data string) {
	_ = notify.AnswerCallback(ctx, token, cbID, "") // stop the button spinner
	lang := r.lang()

	// Destructive-op confirmation (create/delete) replies carry confirm:yes/no.
	switch data {
	case "confirm:yes":
		r.execConfirmed(ctx, token, chatID, lang)
		return
	case "confirm:no":
		r.clearPending(chatID)
		r.reply(ctx, token, chatID, tr(lang, "已取消。", "Cancelled."))
		return
	}

	// Selection flows (user picker, value buttons, create wizard).
	if pfx, rest, ok := splitPrefix(data); ok {
		switch pfx {
		case "act":
			r.startAction(ctx, token, chatID, messageID, lang, rest)
			return
		case "pick":
			r.onPick(ctx, token, chatID, messageID, lang, rest)
			return
		case "pg":
			r.onPage(ctx, token, chatID, messageID, lang, rest)
			return
		case "val":
			r.onValue(ctx, token, chatID, messageID, lang, rest)
			return
		case "ib", "nwq", "nwe", "nw":
			r.onWizard(ctx, token, chatID, messageID, lang, pfx, rest)
			return
		}
	}

	switch strings.TrimPrefix(data, "cmd:") {
	case "summary":
		r.replyPlain(ctx, token, chatID, r.cmdSummary(ctx, lang))
	case "status":
		r.reply(ctx, token, chatID, r.cmdStatus(ctx, lang))
	case "users":
		r.reply(ctx, token, chatID, r.cmdUsers(lang))
	case "top":
		r.reply(ctx, token, chatID, r.cmdTop(lang))
	case "traffic":
		r.reply(ctx, token, chatID, r.cmdTraffic(lang))
	case "node":
		r.reply(ctx, token, chatID, r.cmdNode(lang))
	case "enforce":
		r.reply(ctx, token, chatID, r.cmdEnforce(ctx, lang))
	case "cmdref":
		r.reply(ctx, token, chatID, cmdRef(lang))
	case "help":
		r.sendHelp(ctx, token, chatID, lang)
	}
}

// splitPrefix splits "ns:rest" into ("ns", "rest", true). "rest" may itself
// contain colons (the flows that need it parse further). Returns ok=false when
// there's no colon.
func splitPrefix(data string) (pfx, rest string, ok bool) {
	i := strings.IndexByte(data, ':')
	if i < 0 {
		return "", "", false
	}
	return data[:i], data[i+1:], true
}

// cmdRef is the command syntax reference: every command with its usage. Reached
// via the 📖 button on /help. Shows how to type each command (and its argument)
// for the cases the tappable buttons don't cover yet.
func cmdRef(lang string) string {
	if lang == "zh" {
		return strings.Join([]string{
			"<b>📖 EdgeNest 命令语法</b>",
			"",
			"<b>查询</b>",
			"/概览 — 每日概览(状态+流量+用户数+需关注告警)",
			"/状态 — 引擎/CPU/内存/磁盘/BBR/公网IP",
			"/用户 — 用户列表(总数+逐个用量)",
			"/用户 &lt;email&gt; — 单用户详情(协议/配额用量/到期/开关)",
			"/排行 — 按用量排序的用户排行",
			"/流量 — 服务器流量(当月/总计)",
			"/节点 — 面板访问地址 + 订阅 host 说明",
			"/订阅 &lt;email&gt; — 订阅链接 + 二维码图片",
			"",
			"<b>管理</b>",
			"/启用 &lt;email&gt; — 启用用户",
			"/禁用 &lt;email&gt; — 禁用用户",
			"/配额 &lt;email&gt; &lt;大小&gt; — 设配额(10GB/500MB/0=不限)",
			"/期限 &lt;email&gt; &lt;日期|+N天|0&gt; — 设到期(0=永不)",
			"/重置 &lt;email&gt; — 清空累计用量",
			"/执行 — 立即跑配额/到期检查",
			"/创建 [email] [配额] [天数] — 新建用户(点确认后生效)",
			"/删除 &lt;email&gt; — 删除用户及其订阅(点确认后生效)",
			"",
			"<i>英文命令同样可用:/status /users /user /top /traffic /node /sub /enable /disable /quota /expire /reset /enforce /create /delete</i>",
		}, "\n")
	}
	return strings.Join([]string{
		"<b>📖 EdgeNest command syntax</b>",
		"",
		"<b>Query</b>",
		"/summary — daily digest (status + traffic + user count + alerts)",
		"/status — engine/CPU/mem/disk/BBR/public IP",
		"/users — user list (count + per-user usage)",
		"/user &lt;email&gt; — one user's detail (protocols/quota usage/expiry/state)",
		"/top — users ranked by usage",
		"/traffic — server traffic (this month / total)",
		"/node — panel access addresses + subscription host note",
		"/sub &lt;email&gt; — subscription link + QR image",
		"",
		"<b>Management</b>",
		"/enable &lt;email&gt; — enable a user",
		"/disable &lt;email&gt; — disable a user",
		"/quota &lt;email&gt; &lt;size&gt; — set quota (10GB/500MB/0=unlimited)",
		"/expire &lt;email&gt; &lt;date|+Ndays|0&gt; — set expiry (0=never)",
		"/reset &lt;email&gt; — clear cumulative usage",
		"/enforce — run the quota/expiry check now",
		"/create [email] [quota] [days] — new user (effective after you tap confirm)",
		"/delete &lt;email&gt; — remove a user and its subscriptions (after confirm)",
	}, "\n")
}

// ── read-only command bodies (language-aware) ──────────────────────────────

// helpText is the short header shown above the /help inline keyboard. The full
// per-command usage now lives in cmdRef (the 📖 button) — repeating it as text
// here was redundant, so this is kept to a one-line orientation only.
func (r *Runner) helpText(lang string) string {
	ver := ""
	if digest.AppVersion != "" {
		ver = " v" + digest.AppVersion
	}
	upd := r.updateLine(lang)
	if lang == "zh" {
		return strings.Join([]string{
			"<b>EdgeNest 机器人</b>" + ver + upd,
			"点下方按钮即可操作。带参命令(如 <code>/订阅 &lt;email&gt;</code>)直接输入,",
			"或点「📖 命令语法」看完整用法。",
		}, "\n")
	}
	return strings.Join([]string{
		"<b>EdgeNest bot</b>" + ver + upd,
		"Tap a button below. For commands that take an argument (e.g. <code>/sub &lt;email&gt;</code>)",
		"just type it, or tap “📖 Syntax” for the full usage.",
	}, "\n")
}

// updateLine returns a one-line "newer version available" suffix when the
// update-check cache shows EdgeNest is behind, else "". Reads cache only.
func (r *Runner) updateLine(lang string) string {
	latest, available := updatecheck.Status(r.store, digest.AppVersion)
	if !available {
		return ""
	}
	return tr(lang, "  🆙 有新版 v", "  🆙 v") + esc(latest) + tr(lang, " 可升级", " available")
}

func (r *Runner) cmdStatus(ctx context.Context, lang string) string {
	health, err := r.node.Heartbeat(ctx, r.nodeID)
	if err != nil {
		return tr(lang, "⚠️ 无法获取服务器状态: ", "⚠️ Could not read server status: ") + esc(err.Error())
	}
	eng, _ := r.node.EngineStatus(ctx, r.nodeID)

	// BBR: node-side heartbeat hardcodes "unknown"; read it on the control side.
	bbr := system.ReadBBRState()
	bbrStr := nonEmpty(bbr.CongestionControl, "?")
	if bbr.Enabled {
		bbrStr += " ✓"
	}

	// CPU: prefer an instantaneous percentage; fall back to load average when
	// /proc/stat sampling is unavailable.
	var cpuLine string
	if pct := system.ReadCPUPercent(); pct >= 0 {
		cpuLine = fmt.Sprintf("%s: %.0f%%", tr(lang, "CPU", "CPU"), pct)
	} else {
		cpuLine = fmt.Sprintf("%s: %.2f", tr(lang, "负载(1m)", "load(1m)"), health.CPU)
	}

	// All detected public addresses (dual-stack: list both v4 and v6).
	cap := core.ReadNodeCapability(core.DefaultCapabilityPath)
	var addrs []string
	addrs = append(addrs, cap.IPv4Addrs...)
	if len(cap.IPv4Addrs) == 0 && cap.IPv4Addr != "" {
		addrs = append(addrs, cap.IPv4Addr)
	}
	addrs = append(addrs, cap.IPv6Addrs...)
	if len(cap.IPv6Addrs) == 0 && cap.IPv6Addr != "" {
		addrs = append(addrs, cap.IPv6Addr)
	}
	if len(addrs) == 0 && health.PublicIP != "" {
		addrs = append(addrs, health.PublicIP)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s</b>\n", tr(lang, "📊 服务器状态", "📊 Server status"))
	fmt.Fprintf(&b, "%s: %s (%s)\n", tr(lang, "引擎", "engine"),
		mark(eng.Running, tr(lang, "运行中", "running"), tr(lang, "已停止", "stopped")), esc(nonEmpty(eng.Version, "?")))
	sbN, xrN := digest.EngineInboundCounts(r.store, r.nodeID)
	fmt.Fprintf(&b, "sing-box: %s · xray: %s · BBR: %s\n",
		digest.EngineMark(health.SingboxRunning, sbN, lang), digest.EngineMark(health.XrayRunning, xrN, lang), esc(bbrStr))
	fmt.Fprintf(&b, "%s · %s: %.0f%% · %s: %.0f%%\n", cpuLine,
		tr(lang, "内存", "mem"), health.Mem*100, tr(lang, "磁盘", "disk"), health.Disk*100)
	fmt.Fprintf(&b, "%s:\n", tr(lang, "公网地址", "public addresses"))
	if len(addrs) == 0 {
		b.WriteString("• ?\n")
	}
	for _, a := range addrs {
		fmt.Fprintf(&b, "• <code>%s</code>\n", esc(a))
	}
	if upd := r.updateLine(lang); upd != "" {
		b.WriteString(strings.TrimLeft(upd, " ") + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (r *Runner) cmdUsers(lang string) string {
	emails, err := r.store.AllClientEmails()
	if err != nil {
		return tr(lang, "⚠️ 查询失败: ", "⚠️ Query failed: ") + esc(err.Error())
	}
	if len(emails) == 0 {
		return tr(lang, "暂无用户。", "No users yet.")
	}
	sort.Strings(emails)
	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s (%d)</b>\n", tr(lang, "👥 用户", "👥 Users"), len(emails))
	for _, e := range emails {
		u := r.aggregate(e)
		state := "✅"
		if !u.enabled {
			state = "🚫"
		}
		fmt.Fprintf(&b, "%s <code>%s</code> — %s%s\n", state, esc(e), usageStr(u.usedTotal, u.quota), expiryTag(u.expiry))
	}
	return strings.TrimRight(b.String(), "\n")
}

func (r *Runner) cmdUser(lang, email string) string {
	email = strings.TrimSpace(email)
	if email == "" {
		return tr(lang, "用法: /用户 &lt;email&gt;", "Usage: /user &lt;email&gt;")
	}
	clients, err := r.store.ClientsByEmail(email)
	if err != nil || len(clients) == 0 {
		return tr(lang, "未找到用户 ", "User not found: ") + "<code>" + esc(email) + "</code>"
	}
	u := r.aggregate(email)

	// Protocols across the user's inbounds — one per line for readability.
	var protos []string
	for _, c := range clients {
		if ib, err := r.store.GetInbound(c.InboundID); err == nil && ib != nil {
			protos = append(protos, fmt.Sprintf("• <code>%s</code> (%s)", esc(ib.Type), esc(ib.Tag)))
		}
	}
	protoBlock := tr(lang, "—", "—")
	if len(protos) > 0 {
		protoBlock = "\n" + strings.Join(protos, "\n")
	}

	mUp, mDown, _ := r.store.UserTrafficSince(email, r.monthStart())
	var b strings.Builder
	fmt.Fprintf(&b, "<b>👤 %s</b>\n", esc(email))
	fmt.Fprintf(&b, "%s: %s\n", tr(lang, "状态", "state"), mark(u.enabled, tr(lang, "启用", "enabled"), tr(lang, "禁用", "disabled")))
	fmt.Fprintf(&b, "%s (%d): %s\n", tr(lang, "协议", "protocols"), len(protos), protoBlock)
	fmt.Fprintf(&b, "%s: %s\n", tr(lang, "用量(总)", "usage (total)"), usageStr(u.usedTotal, u.quota))
	fmt.Fprintf(&b, "%s: %s\n", tr(lang, "用量(当月)", "usage (month)"), fmtBytes(mUp+mDown))
	fmt.Fprintf(&b, "%s: %s\n", tr(lang, "期限", "expiry"), r.expiryStr(u.expiry))
	if url := r.subURL(email); url != "" {
		fmt.Fprintf(&b, "%s: <code>%s</code>", tr(lang, "订阅", "subscription"), esc(url))
	} else {
		b.WriteString(tr(lang, "订阅: 无(可在面板创建)", "subscription: none (create in panel)"))
	}
	return b.String()
}

func (r *Runner) cmdTop(lang string) string {
	emails, err := r.store.AllClientEmails()
	if err != nil {
		return tr(lang, "⚠️ 查询失败: ", "⚠️ Query failed: ") + esc(err.Error())
	}
	type row struct {
		email string
		used  int64
	}
	rows := make([]row, 0, len(emails))
	for _, e := range emails {
		u := r.aggregate(e)
		rows = append(rows, row{e, u.usedTotal})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].used > rows[j].used })
	if len(rows) > 10 {
		rows = rows[:10]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s</b>\n", tr(lang, "🏆 流量排行 (Top 10, 总量)", "🏆 Traffic ranking (Top 10, total)"))
	for i, row := range rows {
		fmt.Fprintf(&b, "%d. <code>%s</code> — %s\n", i+1, esc(row.email), fmtBytes(row.used))
	}
	return strings.TrimRight(b.String(), "\n")
}

func (r *Runner) cmdTraffic(lang string) string {
	mUp, mDown, _ := r.store.ServerTrafficSince(r.monthStart())
	var totUp, totDown int64
	if ibs, err := r.store.ListInbounds(r.nodeIDUint()); err == nil {
		for _, ib := range ibs {
			for _, c := range ib.Clients {
				totUp += c.TrafficUp
				totDown += c.TrafficDown
			}
		}
	}
	return fmt.Sprintf(
		"<b>%s</b>\n%s: %s (↑%s ↓%s)\n%s: %s (↑%s ↓%s)",
		tr(lang, "📈 服务器流量", "📈 Server traffic"),
		tr(lang, "当月", "this month"), fmtBytes(mUp+mDown), fmtBytes(mUp), fmtBytes(mDown),
		tr(lang, "总计", "total"), fmtBytes(totUp+totDown), fmtBytes(totUp), fmtBytes(totDown),
	)
}

func (r *Runner) cmdNode(lang string) string {
	cap := core.ReadNodeCapability(core.DefaultCapabilityPath)
	v4 := cap.IPv4Addrs
	if len(v4) == 0 && cap.IPv4Addr != "" {
		v4 = []string{cap.IPv4Addr}
	}
	v6 := cap.IPv6Addrs
	if len(v6) == 0 && cap.IPv6Addr != "" {
		v6 = []string{cap.IPv6Addr}
	}
	panelPath, _ := r.store.GetSetting("panel_path")

	urlFor := func(ip string, v6lit bool) string {
		host := ip
		if v6lit {
			host = "[" + ip + "]"
		}
		if r.panelPort > 0 {
			return fmt.Sprintf("http://%s:%d%s", host, r.panelPort, panelPath)
		}
		return "http://" + host + panelPath
	}

	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s</b>\n", tr(lang, "🖥 面板访问地址", "🖥 Panel access addresses"))
	if len(v4) == 0 && len(v6) == 0 {
		b.WriteString(tr(lang, "未检测到公网地址。\n", "No public address detected.\n"))
	}
	for _, a := range v4 {
		fmt.Fprintf(&b, "• <code>%s</code>\n", esc(urlFor(a, false)))
	}
	for _, a := range v6 {
		fmt.Fprintf(&b, "• <code>%s</code>\n", esc(urlFor(a, true)))
	}
	b.WriteString("\n" + tr(lang,
		"订阅链接的 host 由每条入站在向导里单独选定;用 /订阅 &lt;email&gt; 查具体订阅。",
		"Subscription host is chosen per-inbound in the wizard; use /sub &lt;email&gt; for a user's link."))
	return b.String()
}

func (r *Runner) cmdSub(lang, email string) string {
	email = strings.TrimSpace(email)
	if email == "" {
		return tr(lang, "用法: /订阅 &lt;email&gt;", "Usage: /sub &lt;email&gt;")
	}
	url := r.subURL(email)
	if url == "" {
		return tr(lang, "用户 ", "User ") + "<code>" + esc(email) + "</code>" +
			tr(lang, " 暂无订阅(可在面板创建)。", " has no subscription (create one in the panel).")
	}
	return fmt.Sprintf("<b>🔗 %s %s</b>\n<code>%s</code>", esc(email), tr(lang, "订阅", "subscription"), esc(url))
}

// cmdSubReply sends the subscription link as text, then — when the user has a
// live subscription — pushes its QR code as an inline image so the operator can
// scan it straight into a client. QR generation failing is non-fatal: the text
// link already went out.
func (r *Runner) cmdSubReply(ctx context.Context, token string, chatID int64, lang, email string) {
	r.reply(ctx, token, chatID, r.cmdSub(lang, email))
	url := r.subURL(strings.TrimSpace(email))
	if url == "" {
		return
	}
	png, err := qrcode.Encode(url, qrcode.Medium, 320)
	if err != nil {
		return
	}
	caption := fmt.Sprintf("<code>%s</code>", esc(url))
	_ = notify.SendTelegramPhoto(ctx, token, strconv.FormatInt(chatID, 10), png, caption)
}

// ── helpers ─────────────────────────────────────────────────────────────────

type userAgg struct {
	enabled   bool
	quota     int64
	expiry    int64
	usedTotal int64
}

// aggregate collapses a user's per-inbound clients into one view, matching the
// panel's user list: enabled = any client enabled, quota/expiry = max non-zero,
// used = sum of cumulative counters.
func (r *Runner) aggregate(email string) userAgg {
	clients, _ := r.store.ClientsByEmail(email)
	var u userAgg
	for _, c := range clients {
		if c.Enabled {
			u.enabled = true
		}
		if c.QuotaBytes > u.quota {
			u.quota = c.QuotaBytes
		}
		if c.ExpiryAt > u.expiry {
			u.expiry = c.ExpiryAt
		}
		u.usedTotal += c.TrafficUp + c.TrafficDown
	}
	return u
}

// subURL returns the first non-revoked subscription URL for a user, or "".
func (r *Runner) subURL(email string) string {
	subs, err := r.store.ListSubscriptions()
	if err != nil {
		return ""
	}
	for _, s := range subs {
		if s.Revoked || s.Token == "" {
			continue
		}
		c, err := r.store.GetClient(s.ClientID)
		if err != nil || c == nil || c.Email != email {
			continue
		}
		return r.baseURL() + "/sub/" + s.Token
	}
	return ""
}

// baseURL builds the panel origin for sub links: http://<host>:<port>. Host
// prefers the operator's share_host, then the detected v4/v6 address.
func (r *Runner) baseURL() string {
	host, _ := r.store.GetSetting(keyShareHost)
	if host == "" {
		cap := core.ReadNodeCapability(core.DefaultCapabilityPath)
		switch {
		case len(cap.IPv4Addrs) > 0:
			host = cap.IPv4Addrs[0]
		case cap.IPv4Addr != "":
			host = cap.IPv4Addr
		case len(cap.IPv6Addrs) > 0:
			host = "[" + cap.IPv6Addrs[0] + "]"
		case cap.IPv6Addr != "":
			host = "[" + cap.IPv6Addr + "]"
		default:
			host = "127.0.0.1"
		}
	} else if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]" // bare IPv6
	}
	if r.panelPort > 0 {
		return fmt.Sprintf("http://%s:%d", host, r.panelPort)
	}
	return "http://" + host
}

func (r *Runner) nodeIDUint() uint {
	n, _ := strconv.ParseUint(r.nodeID, 10, 64)
	return uint(n)
}

// location resolves the operator's display timezone (display_tz), falling back
// to the server's local zone. All bot timestamps render in this zone.
func (r *Runner) location() *time.Location {
	if tz, _ := r.store.GetSetting(keyDisplayTZ); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			return loc
		}
	}
	return time.Local
}

// lang returns the panel display language ("zh"/"en") for bot replies, read
// from the default_lang setting; defaults to "en" when unset/unknown.
func (r *Runner) lang() string {
	if v, _ := r.store.GetSetting("default_lang"); v == "zh" {
		return "zh"
	}
	return "en"
}

func (r *Runner) monthStart() string {
	return time.Now().In(r.location()).Format("2006-01") + "-01"
}

func (r *Runner) expiryStr(ts int64) string {
	lang := r.lang()
	if ts <= 0 {
		return tr(lang, "永不过期", "never")
	}
	t := time.Unix(ts, 0).In(r.location())
	stamp := t.Format("2006-01-02 15:04")
	d := time.Until(t)
	if d <= 0 {
		return stamp + tr(lang, " (已过期)", " (expired)")
	}
	return stamp + tr(lang, fmt.Sprintf(" (剩 %d 天)", int(d.Hours()/24)), fmt.Sprintf(" (%d days left)", int(d.Hours()/24)))
}

// expiryTag is a language-neutral marker appended to a user row when expired.
func expiryTag(ts int64) string {
	if ts > 0 && time.Until(time.Unix(ts, 0)) <= 0 {
		return " ⏰"
	}
	return ""
}

func usageStr(used, quota int64) string {
	if quota <= 0 {
		return fmtBytes(used) + " / ∞"
	}
	pct := float64(used) / float64(quota) * 100
	return fmt.Sprintf("%s / %s (%.0f%%)", fmtBytes(used), fmtBytes(quota), pct)
}

func fmtBytes(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%d B", n)
	}
	f := float64(n)
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	i := -1
	for f >= u && i < len(units)-1 {
		f /= u
		i++
	}
	return fmt.Sprintf("%.2f %s", f, units[i])
}

func mark(v bool, yes, no string) string {
	if v {
		return yes
	}
	return no
}

func nonEmpty(s, fb string) string {
	if s == "" {
		return fb
	}
	return s
}

// esc escapes the three characters Telegram HTML parse mode treats specially.
func esc(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func sleep(ctx context.Context, d time.Duration) (cancelled bool) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-t.C:
		return false
	}
}
