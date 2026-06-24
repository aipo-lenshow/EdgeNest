package botrunner

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/notify"
	"github.com/aipo-lenshow/EdgeNest/internal/control/usersvc"
)

// All-selection interaction. Management commands that used to require typing
// an email are now driven entirely by tappable inline keyboards: tap a verb on
// /help → pick a user from a list → (for quota/expiry) pick a value → done. The
// create wizard walks inbound multi-select → quota → expiry → confirm. The only
// place typing is still allowed is the optional user identifier on /create.
//
// callback_data is capped at 64 bytes, so users/inbounds are referenced by index
// into a per-chat snapshot (the session), never by their (possibly long) email.

const (
	pickerPageSize = 8
	sessionTTL     = 5 * time.Minute
)

// session is an in-progress selection flow for one chat. A picker flow uses
// action+emails+page (+ email/stage once a user and a value step are reached); a
// create wizard uses inbounds+selected+newEmail+quota+days+stage.
type session struct {
	action   string   // picker: enable/disable/quota/expire/reset/sub/delete · or "create"
	emails   []string // picker snapshot, index = callback payload
	page     int
	email    string // chosen user (quota/expire two-step)
	stage    string // quota/expire (picker) · ib/quota/expire (wizard)
	inbounds []ibOption
	selected map[uint]bool
	newEmail string // optional custom identifier; "" = auto-numbered
	quota    int64
	days     int
	at       time.Time
}

type ibOption struct {
	id  uint
	tag string
	typ string
}

func (r *Runner) setSession(chatID int64, s *session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sess[chatID] = s
}

// getSession returns the live (non-expired) session, or nil. Expired sessions
// are evicted so a stale tap reports "expired" rather than acting on old state.
func (r *Runner) getSession(chatID int64) *session {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.sess[chatID]
	if s == nil {
		return nil
	}
	if time.Since(s.at) > sessionTTL {
		delete(r.sess, chatID)
		return nil
	}
	return s
}

func (r *Runner) clearSession(chatID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sess, chatID)
}

// edit redraws the callback's own message in place (picker pagination, toggle,
// step advance). Best-effort.
func (r *Runner) edit(ctx context.Context, token string, chatID, messageID int64, html string, rows [][][2]string) {
	_ = notify.EditTelegramKeyboard(ctx, token, strconv.FormatInt(chatID, 10), messageID, html, rows)
}

// editDone freezes a flow's message: replaces it with a result line and strips
// the keyboard so stale buttons can't be tapped again.
func (r *Runner) editDone(ctx context.Context, token string, chatID, messageID int64, html string) {
	r.edit(ctx, token, chatID, messageID, html, nil)
}

// expired reports a stale/missing session by freezing the message.
func (r *Runner) expired(ctx context.Context, token string, chatID, messageID int64, lang string) {
	r.editDone(ctx, token, chatID, messageID,
		tr(lang, "⌛ 操作已过期,请从 /菜单 重新开始。", "⌛ This flow expired; start again from /menu."))
}

// ── #1 launch an action from the /help management buttons ───────────────────

// startAction handles a tapped management verb (act:<action>). Verbs that need a
// user open the user picker; create opens the wizard. The new picker/wizard is a
// fresh message so /help stays intact; its own message id drives later redraws.
func (r *Runner) startAction(ctx context.Context, token string, chatID, messageID int64, lang, action string) {
	switch action {
	case "cancel":
		r.clearSession(chatID)
		r.editDone(ctx, token, chatID, messageID, tr(lang, "已取消。", "Cancelled."))
		return
	case "create":
		r.startCreateWizard(ctx, token, chatID, lang, "")
		return
	}

	emails, err := r.store.AllClientEmails()
	if err != nil {
		r.reply(ctx, token, chatID, tr(lang, "⚠️ 查询失败: ", "⚠️ Query failed: ")+esc(err.Error()))
		return
	}
	if len(emails) == 0 {
		r.reply(ctx, token, chatID, tr(lang, "暂无用户。", "No users yet."))
		return
	}
	sort.Strings(emails)
	r.setSession(chatID, &session{action: action, emails: emails, page: 0, at: time.Now()})
	html, rows := pickerView(lang, action, emails, 0)
	_ = notify.SendTelegramKeyboard(ctx, token, strconv.FormatInt(chatID, 10), html, rows)
}

// ── #2 user picker (index-mapped, paginated) ────────────────────────────────

// pickerView renders one page of the user picker: a title naming the action and
// one button per user (referenced by global index, not email).
func pickerView(lang, action string, emails []string, page int) (string, [][][2]string) {
	pages := (len(emails) + pickerPageSize - 1) / pickerPageSize
	if pages == 0 {
		pages = 1
	}
	if page < 0 {
		page = 0
	}
	if page >= pages {
		page = pages - 1
	}
	start := page * pickerPageSize
	end := start + pickerPageSize
	if end > len(emails) {
		end = len(emails)
	}

	var rows [][][2]string
	for i := start; i < end; i++ {
		rows = append(rows, [][2]string{{trunc(emails[i], 32), "pick:" + strconv.Itoa(i)}})
	}
	// Pagination row (only when multiple pages).
	if pages > 1 {
		var nav [][2]string
		if page > 0 {
			nav = append(nav, [2]string{tr(lang, "◀ 上一页", "◀ Prev"), "pg:" + strconv.Itoa(page-1)})
		}
		nav = append(nav, [2]string{strconv.Itoa(page+1) + "/" + strconv.Itoa(pages), "pg:" + strconv.Itoa(page)})
		if page < pages-1 {
			nav = append(nav, [2]string{tr(lang, "下一页 ▶", "Next ▶"), "pg:" + strconv.Itoa(page+1)})
		}
		rows = append(rows, nav)
	}
	rows = append(rows, [][2]string{{tr(lang, "✖ 取消", "✖ Cancel"), "act:cancel"}})

	title := actionTitle(lang, action) + tr(lang, "(共 ", " (") + strconv.Itoa(len(emails)) + tr(lang, " 个用户)", " users)")
	return "<b>" + title + "</b>", rows
}

// actionTitle is the picker header verb for each management action.
func actionTitle(lang, action string) string {
	switch action {
	case "enable":
		return tr(lang, "选择要启用的用户", "Pick a user to enable")
	case "disable":
		return tr(lang, "选择要禁用的用户", "Pick a user to disable")
	case "quota":
		return tr(lang, "选择要设配额的用户", "Pick a user to set quota")
	case "expire":
		return tr(lang, "选择要设期限的用户", "Pick a user to set expiry")
	case "reset":
		return tr(lang, "选择要清空用量的用户", "Pick a user to reset usage")
	case "sub":
		return tr(lang, "选择要查订阅的用户", "Pick a user for subscription")
	case "delete":
		return tr(lang, "选择要删除的用户", "Pick a user to delete")
	}
	return tr(lang, "选择用户", "Pick a user")
}

// onPage redraws the picker at another page.
func (r *Runner) onPage(ctx context.Context, token string, chatID, messageID int64, lang, rest string) {
	s := r.getSession(chatID)
	if s == nil || s.action == "create" {
		r.expired(ctx, token, chatID, messageID, lang)
		return
	}
	page, err := strconv.Atoi(rest)
	if err != nil {
		return
	}
	s.page = page
	s.at = time.Now()
	html, rows := pickerView(lang, s.action, s.emails, page)
	r.edit(ctx, token, chatID, messageID, html, rows)
}

// onPick handles a tapped user (pick:<idx>). Immediate actions run now; quota/
// expiry advance to a value-button step; delete asks for confirmation.
func (r *Runner) onPick(ctx context.Context, token string, chatID, messageID int64, lang, rest string) {
	s := r.getSession(chatID)
	if s == nil || s.action == "create" {
		r.expired(ctx, token, chatID, messageID, lang)
		return
	}
	idx, err := strconv.Atoi(rest)
	if err != nil || idx < 0 || idx >= len(s.emails) {
		r.expired(ctx, token, chatID, messageID, lang)
		return
	}
	email := s.emails[idx]

	switch s.action {
	case "enable":
		r.clearSession(chatID)
		r.editDone(ctx, token, chatID, messageID, r.cmdSetEnabled(ctx, lang, email, true))
	case "disable":
		r.clearSession(chatID)
		r.editDone(ctx, token, chatID, messageID, r.cmdSetEnabled(ctx, lang, email, false))
	case "reset":
		r.clearSession(chatID)
		r.editDone(ctx, token, chatID, messageID, r.cmdReset(ctx, lang, email))
	case "sub":
		r.clearSession(chatID)
		r.editDone(ctx, token, chatID, messageID, tr(lang, "🔗 订阅 ", "🔗 Subscription ")+"<code>"+esc(email)+"</code>")
		r.cmdSubReply(ctx, token, chatID, lang, email) // link + QR as fresh messages
	case "delete":
		r.clearSession(chatID)
		clients, cerr := r.store.ClientsByEmail(email)
		if cerr != nil || len(clients) == 0 {
			r.editDone(ctx, token, chatID, messageID, tr(lang, "⚠️ 未找到该用户。", "⚠️ User not found."))
			return
		}
		r.editDone(ctx, token, chatID, messageID, tr(lang, "已选择 ", "Selected ")+"<code>"+esc(email)+"</code>")
		r.setPending(chatID, &pendingAction{kind: "delete", email: email, at: time.Now()})
		msg := tr(lang, "⚠️ 确认删除用户 ", "⚠️ Confirm deletion of user ") + "<code>" + esc(email) + "</code>" +
			fmtCount(lang, len(clients))
		r.replyConfirm(ctx, token, chatID, lang, msg)
	case "quota":
		s.email = email
		s.stage = "quota"
		s.at = time.Now()
		r.edit(ctx, token, chatID, messageID, valuePrompt(lang, "quota", email), valueKeyboard(lang, "quota"))
	case "expire":
		s.email = email
		s.stage = "expire"
		s.at = time.Now()
		r.edit(ctx, token, chatID, messageID, valuePrompt(lang, "expire", email), valueKeyboard(lang, "expire"))
	default:
		r.expired(ctx, token, chatID, messageID, lang)
	}
}

func fmtCount(lang string, n int) string {
	return tr(lang, "?(", "? (") + strconv.Itoa(n) +
		tr(lang, " 条凭据 + 订阅将被移除)", " credentials + subscription will be removed)")
}

// ── value buttons (quota / expiry presets) ──────────────────────────────────

func valuePrompt(lang, kind, email string) string {
	who := "<code>" + esc(email) + "</code>"
	if kind == "quota" {
		return tr(lang, "为 ", "Set quota for ") + who + tr(lang, " 选择配额:", ":")
	}
	return tr(lang, "为 ", "Set expiry for ") + who + tr(lang, " 选择期限:", ":")
}

// valueKeyboard is the preset value grid for quota (val:q…) or expiry (val:e…).
// The session's action distinguishes them, so the payload only carries the value
// token. "custom" routes to a typed-command hint (presets cover the common case).
func valueKeyboard(lang, kind string) [][][2]string {
	if kind == "quota" {
		return [][][2]string{
			{{"10 GB", "val:10g"}, {"50 GB", "val:50g"}, {"100 GB", "val:100g"}},
			{{"500 GB", "val:500g"}, {tr(lang, "不限", "Unlimited"), "val:0"}},
			{{tr(lang, "✏️ 自定义", "✏️ Custom"), "val:custom"}, {tr(lang, "✖ 取消", "✖ Cancel"), "act:cancel"}},
		}
	}
	return [][][2]string{
		{{tr(lang, "+30 天", "+30 d"), "val:30"}, {tr(lang, "+90 天", "+90 d"), "val:90"}, {tr(lang, "+365 天", "+365 d"), "val:365"}},
		{{tr(lang, "永不过期", "Never"), "val:0"}},
		{{tr(lang, "✏️ 自定义", "✏️ Custom"), "val:custom"}, {tr(lang, "✖ 取消", "✖ Cancel"), "act:cancel"}},
	}
}

// onValue applies a picked quota/expiry value to the session's chosen user.
func (r *Runner) onValue(ctx context.Context, token string, chatID, messageID int64, lang, rest string) {
	s := r.getSession(chatID)
	if s == nil || s.email == "" || (s.stage != "quota" && s.stage != "expire") {
		r.expired(ctx, token, chatID, messageID, lang)
		return
	}
	email := s.email
	r.clearSession(chatID)

	if rest == "custom" {
		if s.stage == "quota" {
			r.editDone(ctx, token, chatID, messageID,
				tr(lang, "请输入: /配额 ", "Type: /quota ")+"<code>"+esc(email)+tr(lang, " 大小</code>(如 10GB / 0)", " size</code> (e.g. 10GB / 0)"))
		} else {
			r.editDone(ctx, token, chatID, messageID,
				tr(lang, "请输入: /期限 ", "Type: /expire ")+"<code>"+esc(email)+tr(lang, " 日期</code>(如 2026-12-31 / +30 / 0)", " date</code> (e.g. 2026-12-31 / +30 / 0)"))
		}
		return
	}

	if s.stage == "quota" {
		bytes, ok := quotaTokenBytes(rest)
		if !ok {
			r.editDone(ctx, token, chatID, messageID, tr(lang, "⚠️ 无效的配额。", "⚠️ Invalid quota."))
			return
		}
		r.editDone(ctx, token, chatID, messageID, r.cmdQuota(ctx, lang, email+" "+strconv.FormatInt(bytes, 10)))
		return
	}
	// expiry: token is a day count ("0" = never).
	days, err := strconv.Atoi(rest)
	if err != nil {
		r.editDone(ctx, token, chatID, messageID, tr(lang, "⚠️ 无效的期限。", "⚠️ Invalid expiry."))
		return
	}
	arg := "0"
	if days != 0 {
		arg = "+" + strconv.Itoa(days)
	}
	r.editDone(ctx, token, chatID, messageID, r.cmdExpire(ctx, lang, email+" "+arg))
}

// quotaTokenBytes maps a preset token to bytes. "0" → 0 (unlimited).
func quotaTokenBytes(tok string) (int64, bool) {
	switch tok {
	case "0":
		return 0, true
	case "10g":
		return 10 << 30, true
	case "50g":
		return 50 << 30, true
	case "100g":
		return 100 << 30, true
	case "500g":
		return 500 << 30, true
	}
	return 0, false
}

// ── #3 create wizard ────────────────────────────────────────────────────────

// startCreateWizard opens the multi-step create flow: inbound multi-select →
// quota → expiry → confirm. presetEmail (from "/create name") is an optional
// custom identifier; blank auto-numbers. SS/SOCKS inbounds are filtered out (they
// can't host extra shared users).
func (r *Runner) startCreateWizard(ctx context.Context, token string, chatID int64, lang, presetEmail string) {
	inbounds, err := r.store.ListInbounds(r.nodeIDUint())
	if err != nil {
		r.reply(ctx, token, chatID, tr(lang, "⚠️ 查询失败: ", "⚠️ Query failed: ")+esc(err.Error()))
		return
	}
	var opts []ibOption
	for _, ib := range inbounds {
		if !ib.Enabled || usersvc.SkipForMultiUser(ib.Type) {
			continue
		}
		opts = append(opts, ibOption{id: ib.ID, tag: ib.Tag, typ: ib.Type})
	}
	if len(opts) == 0 {
		r.reply(ctx, token, chatID, tr(lang,
			"⚠️ 没有可分配的入站(SS/SOCKS 不支持多用户,或入站均未启用)。",
			"⚠️ No eligible inbound (SS/SOCKS can't host extra users, or none are enabled)."))
		return
	}
	sel := map[uint]bool{}
	for _, o := range opts {
		sel[o.id] = true // default: all selected
	}
	r.setSession(chatID, &session{
		action: "create", stage: "ib", inbounds: opts, selected: sel,
		newEmail: strings.TrimSpace(presetEmail), at: time.Now(),
	})
	html, rows := wizardInboundView(lang, opts, sel)
	_ = notify.SendTelegramKeyboard(ctx, token, strconv.FormatInt(chatID, 10), html, rows)
}

func wizardInboundView(lang string, opts []ibOption, sel map[uint]bool) (string, [][][2]string) {
	var rows [][][2]string
	n := 0
	for i, o := range opts {
		box := "⬜"
		if sel[o.id] {
			box = "✅"
			n++
		}
		label := box + " " + trunc(o.tag, 24) + " (" + o.typ + ")"
		rows = append(rows, [][2]string{{label, "ib:" + strconv.Itoa(i)}})
	}
	rows = append(rows, [][2]string{
		{tr(lang, "下一步 ▶", "Next ▶"), "nw:q"},
		{tr(lang, "✖ 取消", "✖ Cancel"), "nw:cancel"},
	})
	title := tr(lang, "➕ 新建用户 · 选择入站(可多选)", "➕ New user · pick inbounds (multi-select)")
	sub := tr(lang, "已选 ", "Selected ") + strconv.Itoa(n)
	return "<b>" + title + "</b>\n" + sub, rows
}

func wizardQuotaView(lang string) (string, [][][2]string) {
	rows := [][][2]string{
		{{"10 GB", "nwq:10g"}, {"50 GB", "nwq:50g"}, {"100 GB", "nwq:100g"}},
		{{"500 GB", "nwq:500g"}, {tr(lang, "不限", "Unlimited"), "nwq:0"}},
		{{tr(lang, "✖ 取消", "✖ Cancel"), "nw:cancel"}},
	}
	return "<b>" + tr(lang, "➕ 新建用户 · 选择配额", "➕ New user · pick quota") + "</b>", rows
}

func wizardExpiryView(lang string) (string, [][][2]string) {
	rows := [][][2]string{
		{{tr(lang, "+30 天", "+30 d"), "nwe:30"}, {tr(lang, "+90 天", "+90 d"), "nwe:90"}, {tr(lang, "+365 天", "+365 d"), "nwe:365"}},
		{{tr(lang, "永不过期", "Never"), "nwe:0"}},
		{{tr(lang, "✖ 取消", "✖ Cancel"), "nw:cancel"}},
	}
	return "<b>" + tr(lang, "➕ 新建用户 · 选择期限", "➕ New user · pick expiry") + "</b>", rows
}

func (r *Runner) wizardConfirmView(lang string, s *session) (string, [][][2]string) {
	who := s.newEmail
	if who == "" {
		who = tr(lang, "(自动编号)", "(auto-numbered)")
	}
	n := 0
	for _, o := range s.inbounds {
		if s.selected[o.id] {
			n++
		}
	}
	q := tr(lang, "不限", "unlimited")
	if s.quota > 0 {
		q = fmtBytes(s.quota)
	}
	exp := tr(lang, "永不", "never")
	if s.days != 0 {
		exp = strconv.Itoa(s.days) + tr(lang, " 天", " days")
	}
	body := strings.Join([]string{
		"<b>" + tr(lang, "➕ 确认创建用户", "➕ Confirm new user") + "</b>",
		tr(lang, "标识: ", "ID: ") + "<code>" + esc(who) + "</code>",
		tr(lang, "入站: ", "Inbounds: ") + strconv.Itoa(n),
		tr(lang, "配额: ", "Quota: ") + q,
		tr(lang, "期限: ", "Expiry: ") + exp,
	}, "\n")
	rows := [][][2]string{
		{{tr(lang, "✅ 确认创建", "✅ Confirm"), "nw:confirm"}, {tr(lang, "✖ 取消", "✖ Cancel"), "nw:cancel"}},
	}
	return body, rows
}

// onWizard advances the create wizard (ib:/nwq:/nwe:/nw: callbacks).
func (r *Runner) onWizard(ctx context.Context, token string, chatID, messageID int64, lang, pfx, rest string) {
	if pfx == "nw" && rest == "cancel" {
		r.clearSession(chatID)
		r.editDone(ctx, token, chatID, messageID, tr(lang, "已取消。", "Cancelled."))
		return
	}
	s := r.getSession(chatID)
	if s == nil || s.action != "create" {
		r.expired(ctx, token, chatID, messageID, lang)
		return
	}
	s.at = time.Now()

	switch pfx {
	case "ib": // toggle one inbound
		i, err := strconv.Atoi(rest)
		if err != nil || i < 0 || i >= len(s.inbounds) {
			return
		}
		id := s.inbounds[i].id
		s.selected[id] = !s.selected[id]
		html, rows := wizardInboundView(lang, s.inbounds, s.selected)
		r.edit(ctx, token, chatID, messageID, html, rows)
	case "nw": // ib → quota
		if rest == "q" {
			any := false
			for _, o := range s.inbounds {
				if s.selected[o.id] {
					any = true
					break
				}
			}
			if !any {
				html, rows := wizardInboundView(lang, s.inbounds, s.selected)
				r.edit(ctx, token, chatID, messageID,
					html+"\n"+tr(lang, "⚠️ 至少选择一个入站。", "⚠️ Pick at least one inbound."), rows)
				return
			}
			s.stage = "quota"
			html, rows := wizardQuotaView(lang)
			r.edit(ctx, token, chatID, messageID, html, rows)
			return
		}
		if rest == "confirm" {
			r.execWizardCreate(ctx, token, chatID, messageID, lang, s)
		}
	case "nwq": // quota → expiry
		bytes, ok := quotaTokenBytes(rest)
		if !ok {
			return
		}
		s.quota = bytes
		s.stage = "expire"
		html, rows := wizardExpiryView(lang)
		r.edit(ctx, token, chatID, messageID, html, rows)
	case "nwe": // expiry → confirm
		days, err := strconv.Atoi(rest)
		if err != nil {
			return
		}
		s.days = days
		s.stage = "confirm"
		html, rows := r.wizardConfirmView(lang, s)
		r.edit(ctx, token, chatID, messageID, html, rows)
	}
}

// execWizardCreate runs the assembled create and freezes the wizard message with
// the result.
func (r *Runner) execWizardCreate(ctx context.Context, token string, chatID, messageID int64, lang string, s *session) {
	var ids []uint
	for _, o := range s.inbounds {
		if s.selected[o.id] {
			ids = append(ids, o.id)
		}
	}
	r.clearSession(chatID)
	msg := r.execCreate(ctx, lang, usersvc.CreateParams{
		Email:      s.newEmail,
		QuotaBytes: s.quota,
		ExpiryDays: s.days,
		InboundIDs: ids,
	})
	r.editDone(ctx, token, chatID, messageID, msg)
}

// trunc shortens a button label to n runes (Telegram renders long labels but
// keeping them compact avoids wrapping in the picker).
func trunc(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n-1]) + "…"
}
