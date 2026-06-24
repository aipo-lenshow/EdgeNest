package botrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/notify"
	"github.com/aipo-lenshow/EdgeNest/internal/control/quota"
	"github.com/aipo-lenshow/EdgeNest/internal/control/usersvc"
)

// Write commands (allowlist-gated, P4). Mutations route through usersvc so the
// bot and the HTTP API share one code path; destructive ops (create/delete) ask
// for an inline-keyboard confirmation before running.

// pendingTTL bounds how long a confirmation button stays valid.
const pendingTTL = 2 * time.Minute

// pendingAction is a destructive op awaiting the operator's confirm tap.
type pendingAction struct {
	kind  string // "create" | "delete"
	email string
	quota int64
	days  int
	at    time.Time
}

func (r *Runner) setPending(chatID int64, pa *pendingAction) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pend[chatID] = pa
}

func (r *Runner) clearPending(chatID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.pend, chatID)
}

func (r *Runner) takePending(chatID int64) *pendingAction {
	r.mu.Lock()
	defer r.mu.Unlock()
	pa := r.pend[chatID]
	delete(r.pend, chatID)
	return pa
}

// applyFn re-renders DesiredConfig and pushes it to the node. Shared by the user
// service and the manual enforcer. Nil orchestrator (older wiring) → no-op.
func (r *Runner) applyFn() func(context.Context, uint) error {
	return func(ctx context.Context, n uint) error {
		if r.orch == nil {
			return nil
		}
		res, err := r.orch.Apply(ctx, n)
		if err != nil {
			return err
		}
		if !res.OK {
			msg := res.Message
			if res.RolledBack {
				msg = "rolled back: " + msg
			}
			return fmt.Errorf("%s", msg)
		}
		return nil
	}
}

// userSvc builds the shared user mutation service bound to this bot's apply +
// audit sinks.
func (r *Runner) userSvc() *usersvc.Service {
	return &usersvc.Service{
		Store: r.store,
		Apply: r.applyFn(),
		Audit: func(action, resource string, meta map[string]string) { r.audit(action, resource, meta) },
	}
}

// audit records a sensitive bot operation, honoring the panel's audit toggle.
func (r *Runner) audit(action, resource string, meta map[string]string) {
	if v, _ := r.store.GetSetting("audit_enabled"); v == "false" {
		return
	}
	metaJSON := ""
	if meta != nil {
		if b, err := json.Marshal(meta); err == nil {
			metaJSON = string(b)
		}
	}
	_ = r.store.DB().Create(&model.AuditLog{
		Actor:     "telegram",
		Action:    action,
		Resource:  resource,
		Meta:      metaJSON,
		CreatedAt: time.Now().Unix(),
	}).Error
}

// svcErrMsg maps a usersvc.Error to a localized one-line reply.
func svcErrMsg(lang string, err error) string {
	if e, ok := err.(*usersvc.Error); ok {
		switch e.Code {
		case "NOT_FOUND":
			return tr(lang, "⚠️ 未找到该用户。", "⚠️ User not found.")
		case "EMAIL_EXISTS":
			return tr(lang, "⚠️ 该标识已被占用。", "⚠️ That identifier is already taken.")
		case "NO_INBOUND":
			return tr(lang, "⚠️ 没有可分配的入站(SS/SOCKS 不支持多用户)。",
				"⚠️ No eligible inbound (SS/SOCKS can't host extra users).")
		case "APPLY_FAILED":
			return tr(lang, "⚠️ 已改但应用配置失败: ", "⚠️ Saved but applying config failed: ") + esc(e.Msg)
		}
		return "⚠️ " + esc(e.Msg)
	}
	return "⚠️ " + esc(err.Error())
}

// ── non-destructive write commands ──────────────────────────────────────────

func (r *Runner) cmdSetEnabled(ctx context.Context, lang, arg string, enabled bool) string {
	email := strings.TrimSpace(arg)
	if email == "" {
		if enabled {
			return tr(lang, "用法: /启用 &lt;email&gt;", "Usage: /enable &lt;email&gt;")
		}
		return tr(lang, "用法: /禁用 &lt;email&gt;", "Usage: /disable &lt;email&gt;")
	}
	en := enabled
	if _, err := r.userSvc().Update(ctx, r.nodeIDUint(), email, usersvc.UpdateParams{Enabled: &en}); err != nil {
		return svcErrMsg(lang, err)
	}
	if enabled {
		return "✅ " + tr(lang, "已启用 ", "Enabled ") + "<code>" + esc(email) + "</code>"
	}
	return "🚫 " + tr(lang, "已禁用 ", "Disabled ") + "<code>" + esc(email) + "</code>"
}

func (r *Runner) cmdQuota(ctx context.Context, lang, arg string) string {
	fields := strings.Fields(arg)
	if len(fields) < 2 {
		return tr(lang, "用法: /配额 &lt;email&gt; &lt;大小,如 10GB 或 0&gt;",
			"Usage: /quota &lt;email&gt; &lt;size, e.g. 10GB or 0&gt;")
	}
	email := fields[0]
	bytes, ok := parseSize(fields[1])
	if !ok {
		return tr(lang, "⚠️ 无法解析大小,请用 10GB / 500MB / 0。", "⚠️ Could not parse size; use 10GB / 500MB / 0.")
	}
	q := bytes
	if _, err := r.userSvc().Update(ctx, r.nodeIDUint(), email, usersvc.UpdateParams{QuotaBytes: &q}); err != nil {
		return svcErrMsg(lang, err)
	}
	if bytes == 0 {
		return "✅ <code>" + esc(email) + "</code> " + tr(lang, "配额: 不限", "quota: unlimited")
	}
	return "✅ <code>" + esc(email) + "</code> " + tr(lang, "配额: ", "quota: ") + fmtBytes(bytes)
}

func (r *Runner) cmdExpire(ctx context.Context, lang, arg string) string {
	fields := strings.Fields(arg)
	if len(fields) < 2 {
		return tr(lang, "用法: /期限 &lt;email&gt; &lt;日期|+N天|0&gt;",
			"Usage: /expire &lt;email&gt; &lt;date|+Ndays|0&gt;")
	}
	email := fields[0]
	exp, ok := parseExpiry(fields[1], r.location())
	if !ok {
		return tr(lang, "⚠️ 无法解析期限,请用 2026-07-01 / +30 / 0。",
			"⚠️ Could not parse expiry; use 2026-07-01 / +30 / 0.")
	}
	if _, err := r.userSvc().Update(ctx, r.nodeIDUint(), email, usersvc.UpdateParams{ExpiryAt: &exp}); err != nil {
		return svcErrMsg(lang, err)
	}
	if exp == 0 {
		return "✅ <code>" + esc(email) + "</code> " + tr(lang, "已设为永不过期。", "set to never expire.")
	}
	stamp := time.Unix(exp, 0).In(r.location()).Format("2006-01-02 15:04")
	return "✅ <code>" + esc(email) + "</code> " + tr(lang, "到期: ", "expires: ") + esc(stamp)
}

func (r *Runner) cmdReset(ctx context.Context, lang, arg string) string {
	email := strings.TrimSpace(arg)
	if email == "" {
		return tr(lang, "用法: /重置 &lt;email&gt;", "Usage: /reset &lt;email&gt;")
	}
	yes := true
	if _, err := r.userSvc().Update(ctx, r.nodeIDUint(), email, usersvc.UpdateParams{ResetUsage: &yes}); err != nil {
		return svcErrMsg(lang, err)
	}
	return "✅ " + tr(lang, "已重置用量 ", "Usage reset ") + "<code>" + esc(email) + "</code>"
}

func (r *Runner) cmdEnforce(ctx context.Context, lang string) string {
	enf := &quota.Enforcer{
		Store: r.store,
		Apply: r.applyFn(),
		Audit: func(action, resource string, meta map[string]string) { r.audit(action, resource, meta) },
	}
	res, err := enf.EnforceAll(ctx, r.nodeIDUint())
	if err != nil {
		return tr(lang, "⚠️ 执行失败: ", "⚠️ Enforcement failed: ") + esc(err.Error())
	}
	if len(res.Disabled) == 0 {
		return tr(lang, "✅ 检查完成,无用户需要禁用。", "✅ Check complete; no users needed disabling.")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "✅ %s%d\n", tr(lang, "检查完成,已禁用 ", "Check complete; disabled "), len(res.Disabled))
	for _, d := range res.Disabled {
		fmt.Fprintf(&b, "🚫 <code>%s</code> — %s\n", esc(d.Email), esc(d.Reason))
	}
	return strings.TrimRight(b.String(), "\n")
}

// ── destructive write commands (inline-keyboard confirmation) ───────────────

func (r *Runner) cmdDeletePrompt(ctx context.Context, token string, chatID int64, lang, arg string) {
	email := strings.TrimSpace(arg)
	if email == "" {
		r.reply(ctx, token, chatID, tr(lang, "用法: /删除 &lt;email&gt;", "Usage: /delete &lt;email&gt;"))
		return
	}
	clients, err := r.store.ClientsByEmail(email)
	if err != nil || len(clients) == 0 {
		r.reply(ctx, token, chatID, tr(lang, "⚠️ 未找到该用户。", "⚠️ User not found."))
		return
	}
	r.setPending(chatID, &pendingAction{kind: "delete", email: email, at: time.Now()})
	msg := tr(lang, "⚠️ 确认删除用户 ", "⚠️ Confirm deletion of user ") + "<code>" + esc(email) + "</code>" +
		fmt.Sprintf(tr(lang, "?(%d 条凭据 + 订阅将被移除)", "? (%d credentials + subscription will be removed)"), len(clients))
	r.replyConfirm(ctx, token, chatID, lang, msg)
}

func (r *Runner) cmdCreatePrompt(ctx context.Context, token string, chatID int64, lang, arg string) {
	fields := strings.Fields(arg)
	// 0 or 1 args (no quota/days typed) → guided wizard, "select don't fill"
	// (P7). The lone optional arg is the custom identifier. Power users who type
	// the full "/create email quota days" still get the one-tap confirm below.
	if len(fields) < 2 {
		preset := ""
		if len(fields) == 1 {
			preset = fields[0]
		}
		r.startCreateWizard(ctx, token, chatID, lang, preset)
		return
	}
	pa := &pendingAction{kind: "create", at: time.Now()}
	if len(fields) >= 1 {
		pa.email = fields[0]
	}
	if len(fields) >= 2 {
		b, ok := parseSize(fields[1])
		if !ok {
			r.reply(ctx, token, chatID, tr(lang, "⚠️ 无法解析配额,请用 10GB / 0。", "⚠️ Could not parse quota; use 10GB / 0."))
			return
		}
		pa.quota = b
	}
	if len(fields) >= 3 {
		d, ok := parseDays(fields[2])
		if !ok {
			r.reply(ctx, token, chatID, tr(lang, "⚠️ 无法解析天数,请用 30 或 +30。", "⚠️ Could not parse days; use 30 or +30."))
			return
		}
		pa.days = d
	}
	who := pa.email
	if who == "" {
		who = tr(lang, "(自动编号)", "(auto-numbered)")
	}
	q := tr(lang, "不限", "unlimited")
	if pa.quota > 0 {
		q = fmtBytes(pa.quota)
	}
	exp := tr(lang, "永不", "never")
	if pa.days != 0 {
		exp = fmt.Sprintf(tr(lang, "%d 天", "%d days"), pa.days)
	}
	r.setPending(chatID, pa)
	msg := fmt.Sprintf(
		tr(lang, "➕ 确认创建用户 %s(配额 %s · 期限 %s · 全部启用的入站)?",
			"➕ Confirm creation of user %s (quota %s · expiry %s · all enabled inbounds)?"),
		"<code>"+esc(who)+"</code>", q, exp)
	r.replyConfirm(ctx, token, chatID, lang, msg)
}

// replyConfirm sends a message with confirm / cancel inline buttons.
func (r *Runner) replyConfirm(ctx context.Context, token string, chatID int64, lang, html string) {
	buttons := [][][2]string{
		{{tr(lang, "✅ 确认", "✅ Confirm"), "confirm:yes"}, {tr(lang, "取消", "Cancel"), "confirm:no"}},
	}
	_ = notify.SendTelegramKeyboard(ctx, token, strconv.FormatInt(chatID, 10), html, buttons)
}

// execConfirmed runs the pending destructive op behind a tapped confirm button.
func (r *Runner) execConfirmed(ctx context.Context, token string, chatID int64, lang string) {
	pa := r.takePending(chatID)
	if pa == nil || time.Since(pa.at) > pendingTTL {
		r.reply(ctx, token, chatID, tr(lang, "确认已过期,请重新发起命令。", "Confirmation expired; please re-issue the command."))
		return
	}
	switch pa.kind {
	case "delete":
		res, err := r.userSvc().Delete(ctx, r.nodeIDUint(), pa.email)
		if err != nil {
			r.reply(ctx, token, chatID, svcErrMsg(lang, err))
			return
		}
		r.reply(ctx, token, chatID, fmt.Sprintf("🗑 %s<code>%s</code> (%d %s)",
			tr(lang, "已删除 ", "Deleted "), esc(res.Email), res.DeletedClients, tr(lang, "条凭据", "credentials")))
	case "create":
		r.reply(ctx, token, chatID, r.execCreate(ctx, lang, usersvc.CreateParams{
			Email:      pa.email,
			QuotaBytes: pa.quota,
			ExpiryDays: pa.days,
		}))
	}
}

// execCreate runs a user creation through the shared service and renders the
// localized result line (created + inbound count + subscription URL + any
// skipped SS/SOCKS inbounds), or a localized error. Shared by the /create
// confirm path and the create wizard.
func (r *Runner) execCreate(ctx context.Context, lang string, p usersvc.CreateParams) string {
	res, err := r.userSvc().Create(ctx, r.nodeIDUint(), p)
	if err != nil {
		return svcErrMsg(lang, err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "✅ %s<code>%s</code> (%d %s)\n",
		tr(lang, "已创建 ", "Created "), esc(res.Email), len(res.InboundTags), tr(lang, "个入站", "inbounds"))
	if url := r.subURL(res.Email); url != "" {
		fmt.Fprintf(&b, "%s: <code>%s</code>", tr(lang, "订阅", "subscription"), esc(url))
	}
	if len(res.Skipped) > 0 {
		fmt.Fprintf(&b, "\n%s: %s", tr(lang, "跳过(SS/SOCKS)", "skipped (SS/SOCKS)"), esc(strings.Join(res.Skipped, ", ")))
	}
	return strings.TrimRight(b.String(), "\n")
}

// ── parsing helpers ─────────────────────────────────────────────────────────

// parseSize parses a human size into bytes. "0"/""/unlimited → 0. Accepts a
// number with an optional binary unit suffix (B/K/KB/M/MB/G/GB/T/TB/P/PB); a
// bare number is treated as bytes.
func parseSize(s string) (int64, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "0", "", "unlimited", "无限", "不限", "∞", "none":
		return 0, true
	}
	i := 0
	for i < len(s) && (s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	numStr := s[:i]
	unit := strings.TrimSpace(s[i:])
	if numStr == "" {
		return 0, false
	}
	num, err := strconv.ParseFloat(numStr, 64)
	if err != nil || num < 0 {
		return 0, false
	}
	var mult float64
	switch unit {
	case "", "b":
		mult = 1
	case "k", "kb", "kib":
		mult = 1 << 10
	case "m", "mb", "mib":
		mult = 1 << 20
	case "g", "gb", "gib":
		mult = 1 << 30
	case "t", "tb", "tib":
		mult = 1 << 40
	case "p", "pb", "pib":
		mult = 1 << 50
	default:
		return 0, false
	}
	return int64(num * mult), true
}

// parseDays parses a "+30" / "30" / "30天" day count for the create flow.
func parseDays(s string) (int, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimPrefix(s, "+")
	s = strings.TrimSuffix(s, "days")
	s = strings.TrimSuffix(s, "day")
	s = strings.TrimSuffix(s, "d")
	s = strings.TrimSuffix(s, "天")
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, false
	}
	return n, true
}

// parseExpiry resolves an expiry argument to an absolute unix timestamp (end of
// the target day in loc). "0"/never → 0. "+N[days]" → end of today+N. An
// absolute "YYYY-MM-DD" → end of that date. A past date yields a past instant
// (already expired), which is intentional for testing.
func parseExpiry(s string, loc *time.Location) (int64, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "0", "never", "永不", "永久":
		return 0, true
	}
	if strings.HasPrefix(s, "+") {
		n, ok := parseDays(s)
		if !ok {
			return 0, false
		}
		t := time.Now().In(loc).AddDate(0, 0, n)
		return endOfDay(t, loc), true
	}
	t, err := time.ParseInLocation("2006-01-02", s, loc)
	if err != nil {
		return 0, false
	}
	return endOfDay(t, loc), true
}

func endOfDay(t time.Time, loc *time.Location) int64 {
	return time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, loc).Unix()
}
