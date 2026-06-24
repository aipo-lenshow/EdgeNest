#!/usr/bin/env bash
#
# EdgeNest uninstaller.
#
# Behaviour:
#   * If EdgeNest is NOT installed, prints a bilingual "nothing to remove"
#     message and exits 0.  (Orphan data dirs are surfaced as a hint.)
#   * If installed, asks the user TWO questions:
#       Q1  Keep your data for a future re-install?  [Y/n]
#       Q2  Confirm uninstall?                       [y/N]
#     …then does the work.  No info dump in between.
#
# Flags (all skip the matching prompt):
#   --purge / -p    delete /etc/edgenest + /var/log/edgenest as well
#   --yes / -y      non-interactive (defaults to "keep data" unless --purge)
#   --lang=en|zh|zh-TW|fa|ru|vi  force language; otherwise auto-detect or interactive pick

set -euo pipefail

INSTALL_BIN="/usr/local/bin"
DATA_DIR="/etc/edgenest"
LOG_DIR="/var/log/edgenest"
XRAY_SHARE_DIR="/usr/local/share/xray"
SYSTEMD_UNIT="/etc/systemd/system/edgenest.service"

if [ -t 1 ]; then
  C_RED='\033[31m'; C_GREEN='\033[32m'; C_YELLOW='\033[33m'
  C_CYAN='\033[36m'; C_DIM='\033[2m'; C_RESET='\033[0m'
else
  C_RED=''; C_GREEN=''; C_YELLOW=''; C_CYAN=''; C_DIM=''; C_RESET=''
fi
red()    { printf "${C_RED}%s${C_RESET}\n" "$*"; }
green()  { printf "${C_GREEN}%s${C_RESET}\n" "$*"; }
yellow() { printf "${C_YELLOW}%s${C_RESET}\n" "$*"; }
info()   { printf "${C_CYAN}▶ %s${C_RESET}\n" "$*"; }

PURGE=0
ASSUME_YES=0
LANG_CHOICE=""
for arg in "$@"; do
  case "$arg" in
    --purge|-p) PURGE=1 ;;
    --yes|-y)   ASSUME_YES=1 ;;
    --lang=en|--lang=zh|--lang=zh-TW|--lang=fa|--lang=ru|--lang=vi) LANG_CHOICE="${arg#--lang=}" ;;
    -h|--help)
      cat <<EOF
EdgeNest uninstaller

Usage:
  sudo bash scripts/uninstall.sh                  interactive, asks 2 questions
  sudo bash scripts/uninstall.sh --purge          also delete ${DATA_DIR} + ${LOG_DIR}
  sudo bash scripts/uninstall.sh --yes            non-interactive (default: keep data)
  sudo bash scripts/uninstall.sh --yes --purge    automated full wipe
  sudo bash scripts/uninstall.sh --lang=en        force language (en|zh|zh-TW|fa|ru|vi)
EOF
      exit 0
      ;;
  esac
done

if [ "$(id -u)" -ne 0 ]; then
  red "Please run as root.  /  请以 root 身份运行。"
  red "  sudo bash scripts/uninstall.sh"
  exit 1
fi

# ── Detect install status ────────────────────────────────────────────
# "Installed" = systemd unit present OR edgenest binary present.
# Orphan data dirs alone don't count — they're handled separately.
INSTALLED=0
if [ -f "$SYSTEMD_UNIT" ] || [ -x "$INSTALL_BIN/edgenest" ]; then
  INSTALLED=1
fi

# ── Not installed → bilingual notice and exit (unless --purge) ───────
if [ "$INSTALLED" = "0" ] && [ "$PURGE" != "1" ]; then
  echo ""
  green "✓ EdgeNest is not installed — nothing to remove."
  green "✓ EdgeNest 未安装 — 无需卸载。"
  if [ -d "$DATA_DIR" ] || [ -d "$LOG_DIR" ]; then
    echo ""
    yellow "Orphan data left at ${DATA_DIR} / ${LOG_DIR}."
    yellow "残留数据在 ${DATA_DIR} / ${LOG_DIR} 。"
    yellow "  Run: sudo bash scripts/uninstall.sh --purge --yes   to delete it."
    yellow "  执行: sudo bash scripts/uninstall.sh --purge --yes   可清理掉。"
  fi
  echo ""
  exit 0
fi

# ── Language: explicit > env > interactive > $LANG > en ──────────────
detect_default_lang() {
  if [ -n "${EDGENEST_LANG:-}" ]; then printf "%s" "$EDGENEST_LANG"; return; fi
  case "${LANG:-}${LC_ALL:-}${LC_MESSAGES:-}" in
    *zh_TW*|*zh_HK*|*zh-TW*) printf "zh-TW" ;;
    *zh*|*ZH*) printf "zh" ;;
    *fa*|*fa_IR*) printf "fa" ;;
    *ru*|*RU*) printf "ru" ;;
    *vi*|*vi_VN*) printf "vi" ;;
    *)         printf "en" ;;
  esac
}
if [ -z "$LANG_CHOICE" ]; then
  default_lang=$(detect_default_lang)
  if [ "$ASSUME_YES" = "1" ] || [ ! -e /dev/tty ]; then
    LANG_CHOICE="$default_lang"
  else
    printf "\n${C_CYAN}? Language / 语言${C_RESET} [1] English  [2] 中文  [3] 繁體中文  [4] فارسی  [5] Русский  [6] Tiếng Việt  (default: %s): " "$default_lang"
    read -r lang_input </dev/tty || lang_input=""
    case "$lang_input" in
      1|en|EN|english|English)              LANG_CHOICE="en" ;;
      2|zh|ZH|cn|CN|chinese|Chinese|中文)   LANG_CHOICE="zh" ;;
      3|zh-TW|zh-tw|zh_TW|zhtw|ZHTW|tw|TW|繁體中文|繁体中文|繁中)  LANG_CHOICE="zh-TW" ;;
      4|fa|FA|farsi|Farsi|persian|Persian|فارسی)                 LANG_CHOICE="fa" ;;
      5|ru|RU|russian|Russian|Русский|русский)                   LANG_CHOICE="ru" ;;
      6|vi|VI|vn|VN|vietnamese|Vietnamese|"tiếng việt"|"Tiếng Việt"|"tieng viet")  LANG_CHOICE="vi" ;;
      *)                                    LANG_CHOICE="$default_lang" ;;
    esac
  fi
fi

# ── Minimal i18n: only what the two questions + result need ──────────
declare -A T_EN T_ZH T_ZHTW T_FA T_RU T_VI
T_EN[q_keep]="? Keep your data (DB / certs / logs) so a future re-install can reuse it? [Y/n]: "
T_ZH[q_keep]="? 是否保留数据 (数据库 / 证书 / 日志), 以便未来重装时继续使用? [Y/n]: "
T_EN[q_confirm_keep]="? Uninstall now? (data will be kept) [y/N]: "
T_ZH[q_confirm_keep]="? 确认现在卸载? (数据将保留) [y/N]: "
T_EN[q_confirm_purge]="? Uninstall now AND DELETE ALL DATA? (irreversible) [y/N]: "
T_ZH[q_confirm_purge]="? 确认现在卸载, 并彻底删除所有数据? (不可恢复) [y/N]: "
T_EN[cancelled]="Cancelled."
T_ZH[cancelled]="已取消。"
T_EN[step_svc]="Stopping + disabling edgenest.service…"
T_ZH[step_svc]="停止并禁用 edgenest.service…"
T_EN[step_bin]="Removing program files (edgenest / sing-box / xray)…"
T_ZH[step_bin]="删除程序文件 (edgenest / sing-box / xray)…"
T_EN[step_purge]="Deleting all data (config + logs)…"
T_ZH[step_purge]="删除所有数据 (配置 + 日志)…"
T_EN[step_keep_note]="Keeping data dirs (%s, %s) — re-install will reuse them. Add --purge to wipe."
T_ZH[step_keep_note]="保留数据目录 (%s, %s) — 重装会自动继续使用。加 --purge 可彻底清除。"
T_EN[done_keep]="✓ EdgeNest uninstalled.  Data kept at %s and %s — re-install will reuse it."
T_ZH[done_keep]="✓ EdgeNest 已卸载。  数据保留在 %s 与 %s — 重装会自动继续使用。"
T_EN[done_purge]="✓ EdgeNest uninstalled and all data deleted."
T_ZH[done_purge]="✓ EdgeNest 已卸载, 所有数据已删除。"

# ---- 繁體中文 (zh-TW) ----
T_ZHTW[q_keep]="? 是否保留資料 (資料庫 / 憑證 / 日誌), 以便未來重裝時繼續使用? [Y/n]: "
T_ZHTW[q_confirm_keep]="? 確認現在解除安裝? (資料將保留) [y/N]: "
T_ZHTW[q_confirm_purge]="? 確認現在解除安裝, 並徹底刪除所有資料? (無法復原) [y/N]: "
T_ZHTW[cancelled]="已取消。"
T_ZHTW[step_svc]="停止並停用 edgenest.service…"
T_ZHTW[step_bin]="刪除程式檔案 (edgenest / sing-box / xray)…"
T_ZHTW[step_purge]="刪除所有資料 (設定 + 日誌)…"
T_ZHTW[step_keep_note]="保留資料目錄 (%s, %s) — 重裝會自動繼續使用。加 --purge 可徹底清除。"
T_ZHTW[done_keep]="✓ EdgeNest 已解除安裝。  資料保留在 %s 與 %s — 重裝會自動繼續使用。"
T_ZHTW[done_purge]="✓ EdgeNest 已解除安裝, 所有資料已刪除。"

# ---- فارسی (fa) ----
T_FA[q_keep]="? داده‌های خود (پایگاه‌داده / گواهی‌ها / لاگ‌ها) را نگه می‌دارید تا نصب مجدد بعدی از آن‌ها استفاده کند؟ [Y/n]: "
T_FA[q_confirm_keep]="? هم‌اکنون حذف شود؟ (داده‌ها نگه داشته می‌شوند) [y/N]: "
T_FA[q_confirm_purge]="? هم‌اکنون حذف و همهٔ داده‌ها پاک شوند؟ (غیرقابل بازگشت) [y/N]: "
T_FA[cancelled]="لغو شد."
T_FA[step_svc]="در حال توقف و غیرفعال‌سازی edgenest.service…"
T_FA[step_bin]="در حال حذف فایل‌های برنامه (edgenest / sing-box / xray)…"
T_FA[step_purge]="در حال حذف همهٔ داده‌ها (پیکربندی + لاگ‌ها)…"
T_FA[step_keep_note]="نگه داشتن پوشه‌های داده (%s، %s) — نصب مجدد از آن‌ها استفاده می‌کند. برای پاک‌سازی --purge اضافه کنید."
T_FA[done_keep]="✓ EdgeNest حذف شد.  داده‌ها در %s و %s نگه داشته شد — نصب مجدد از آن استفاده می‌کند."
T_FA[done_purge]="✓ EdgeNest حذف شد و همهٔ داده‌ها پاک شدند."

# ---- Русский (ru) ----
T_RU[q_keep]="? Сохранить ваши данные (БД / сертификаты / логи), чтобы будущая переустановка могла их использовать? [Y/n]: "
T_RU[q_confirm_keep]="? Удалить сейчас? (данные будут сохранены) [y/N]: "
T_RU[q_confirm_purge]="? Удалить сейчас И УДАЛИТЬ ВСЕ ДАННЫЕ? (необратимо) [y/N]: "
T_RU[cancelled]="Отменено."
T_RU[step_svc]="Остановка и отключение edgenest.service…"
T_RU[step_bin]="Удаление файлов программы (edgenest / sing-box / xray)…"
T_RU[step_purge]="Удаление всех данных (конфигурация + логи)…"
T_RU[step_keep_note]="Каталоги данных сохранены (%s, %s) — переустановка их использует. Добавьте --purge для удаления."
T_RU[done_keep]="✓ EdgeNest удалён.  Данные сохранены в %s и %s — переустановка их использует."
T_RU[done_purge]="✓ EdgeNest удалён, все данные удалены."

# ---- Tiếng Việt (vi) ----
T_VI[q_keep]="? Giữ dữ liệu của bạn (CSDL / chứng chỉ / nhật ký) để lần cài đặt lại sau có thể dùng lại? [Y/n]: "
T_VI[q_confirm_keep]="? Gỡ cài đặt ngay? (dữ liệu sẽ được giữ) [y/N]: "
T_VI[q_confirm_purge]="? Gỡ cài đặt ngay VÀ XÓA TẤT CẢ DỮ LIỆU? (không thể khôi phục) [y/N]: "
T_VI[cancelled]="Đã hủy."
T_VI[step_svc]="Đang dừng + tắt edgenest.service…"
T_VI[step_bin]="Đang xóa tệp chương trình (edgenest / sing-box / xray)…"
T_VI[step_purge]="Đang xóa tất cả dữ liệu (cấu hình + nhật ký)…"
T_VI[step_keep_note]="Giữ các thư mục dữ liệu (%s, %s) — cài đặt lại sẽ dùng lại chúng. Thêm --purge để xóa sạch."
T_VI[done_keep]="✓ Đã gỡ cài đặt EdgeNest.  Dữ liệu được giữ tại %s và %s — cài đặt lại sẽ dùng lại."
T_VI[done_purge]="✓ Đã gỡ cài đặt EdgeNest và xóa tất cả dữ liệu."

# Always-true note: the installer source folder is never auto-removed.
T_EN[done_residual_hint]="Note: the source folder you ran the installer from (e.g. ~/EdgeNest) was not removed — delete it manually if you no longer need it."
T_ZH[done_residual_hint]="提示: 你运行安装脚本的源码目录 (如 ~/EdgeNest) 未被删除 — 不需要可手动删除。"
T_ZHTW[done_residual_hint]="提示: 你執行安裝腳本的原始碼目錄 (如 ~/EdgeNest) 未被刪除 — 不需要可手動刪除。"
T_FA[done_residual_hint]="توجه: پوشهٔ سورسی که نصب‌کننده را از آن اجرا کردید (مثلاً ~/EdgeNest) حذف نشد — اگر نیاز ندارید دستی پاک کنید."
T_RU[done_residual_hint]="Примечание: папка с исходным кодом, из которой вы запускали установщик (например, ~/EdgeNest), не удалена — удалите её вручную, если она больше не нужна."
T_VI[done_residual_hint]="Lưu ý: thư mục mã nguồn bạn chạy trình cài đặt (ví dụ ~/EdgeNest) chưa được xóa — hãy xóa thủ công nếu không cần."

# Conditional note: shown ONLY when a pre-1.03 plaintext credential banner is
# actually found in the journal (see detection at the bottom). Current builds
# never trigger this, so a clean install is never nagged about a non-existent leak.
T_EN[done_journald_hint]="Heads-up: an older build logged first-run credentials to the system journal. To purge them: journalctl --rotate && journalctl --vacuum-time=1s (this clears ALL system logs)."
T_ZH[done_journald_hint]="注意: 旧版本曾把首次凭据写入系统日志 (journald)。清除: journalctl --rotate && journalctl --vacuum-time=1s (会清空全部系统日志)。"
T_ZHTW[done_journald_hint]="注意: 舊版本曾把首次憑據寫入系統日誌 (journald)。清除: journalctl --rotate && journalctl --vacuum-time=1s (會清空全部系統日誌)。"
T_FA[done_journald_hint]="توجه: نسخهٔ قدیمی اعتبارنامهٔ اولین اجرا را در ژورنال سیستم ثبت کرده بود. برای پاک‌سازی: journalctl --rotate && journalctl --vacuum-time=1s (همهٔ لاگ‌های سیستم پاک می‌شود)."
T_RU[done_journald_hint]="Внимание: старая версия записывала учётные данные первого запуска в системный журнал. Очистить: journalctl --rotate && journalctl --vacuum-time=1s (удаляет ВСЕ системные логи)."
T_VI[done_journald_hint]="Chú ý: phiên bản cũ đã ghi thông tin đăng nhập lần chạy đầu vào nhật ký hệ thống. Để xóa: journalctl --rotate && journalctl --vacuum-time=1s (xóa TẤT CẢ nhật ký hệ thống)."

t() {
  local key="$1"; shift
  local raw
  case "$LANG_CHOICE" in
    zh)    raw="${T_ZH[$key]:-${T_EN[$key]:-$key}}" ;;
    zh-TW) raw="${T_ZHTW[$key]:-${T_EN[$key]:-$key}}" ;;
    fa)    raw="${T_FA[$key]:-${T_EN[$key]:-$key}}" ;;
    ru)    raw="${T_RU[$key]:-${T_EN[$key]:-$key}}" ;;
    vi)    raw="${T_VI[$key]:-${T_EN[$key]:-$key}}" ;;
    *)     raw="${T_EN[$key]:-$key}" ;;
  esac
  # shellcheck disable=SC2059
  printf "$raw" "$@"
}

# ── Q1: keep or purge (skip if --purge or --yes already chose) ──────
if [ "$PURGE" = "1" ] || [ "$ASSUME_YES" = "1" ]; then
  :
else
  echo ""
  printf "${C_CYAN}%s${C_RESET}" "$(t q_keep)"
  read -r keep_ans </dev/tty || keep_ans=""
  case "$keep_ans" in
    n|N|no|NO|No) PURGE=1 ;;
    *)            PURGE=0 ;;
  esac
fi

# ── Q2: final confirm (skip if --yes) ───────────────────────────────
if [ "$ASSUME_YES" != "1" ]; then
  echo ""
  if [ "$PURGE" = "1" ]; then
    printf "${C_RED}%s${C_RESET}" "$(t q_confirm_purge)"
  else
    printf "${C_CYAN}%s${C_RESET}" "$(t q_confirm_keep)"
  fi
  read -r confirm_ans </dev/tty || confirm_ans=""
  case "$confirm_ans" in
    y|Y|yes|YES|Yes) : ;;
    *)               echo ""; red "$(t cancelled)"; exit 0 ;;
  esac
fi

# ── Execute ─────────────────────────────────────────────────────────
# From here on this is BEST-EFFORT cleanup: every step must run even if an
# earlier one fails, so the operator is never left half-uninstalled. Disable
# errexit (set +e) for the whole execute section — under the script's
# `set -euo pipefail`, a single non-zero rc (e.g. a firewall grep that matches
# nothing on a v4-only host, or rm of an already-absent file) would otherwise
# ABORT the uninstall midway, stranding data dirs + sysctl tweaks after the
# binary is already gone. Each step still guards its own critical failures.
set +e
echo ""
info "$(t step_svc)"
systemctl stop edgenest 2>/dev/null || true
systemctl disable edgenest 2>/dev/null || true
rm -f "$SYSTEMD_UNIT"
systemctl daemon-reload
# Belt-and-suspenders: reap any edgenest DAEMON not tracked by systemd (e.g. a
# manually launched instance that escaped the cgroup). Match on '--role': the
# daemon always carries it (the systemd unit and `make run` both pass --role),
# while the operator-facing CLI/menu (bare `edgenest`, `edgenest uninstall`)
# never does. A plain `pkill -x edgenest` would also kill the very menu process
# that launched this uninstall (option 5 → bash uninstall.sh), printing
# "Terminated" and dropping the operator to a shell mid-run. The binary is
# removed just below, so a reaped daemon won't respawn.
pkill -f 'edgenest --role' 2>/dev/null || true
# The daemon has no graceful-shutdown handler, so killing it orphans its engine
# children (sing-box / xray / cloudflared) — they keep running, reparented to
# init, still binding proxy ports, and survive the whole uninstall. Reap them by
# exact binary name (-x, so the uninstaller's own `bash` shell — which has these
# words in its argv — can never be self-matched) now that the daemon is gone and
# can't respawn them. Single-purpose node box: no unrelated sing-box/xray here.
pkill -x sing-box    2>/dev/null || true
pkill -x xray        2>/dev/null || true
pkill -x cloudflared 2>/dev/null || true

info "$(t step_bin "$INSTALL_BIN")"
rm -f "$INSTALL_BIN/edgenest"
rm -f "$INSTALL_BIN/sing-box"
rm -f "$INSTALL_BIN/xray"
rm -rf "$XRAY_SHARE_DIR"

# Flush every edgenest-managed iptables rule (filter INPUT port-ACCEPT +
# nat PREROUTING port-hopping REDIRECT), for both IPv4 and IPv6. With the daemon
# gone these can't be reconciled away, so clean them here. We delete ONLY our
# `edgenest-managed`-tagged lines, one at a time, off the LIVE table — the
# operator's own rules and any docker / fail2ban chains are never touched. The
# whole section runs under set +e (above), so an empty ip6tables on a v4-only
# host returning rc=1 can never abort the uninstall.
#
# Robustness against transient nf_tables errors: right after the proxy engines
# are killed above (sing-box drives the kernel hard via tun / gvisor / netlink),
# the nf_tables subsystem is briefly unsettled and iptables-nft commands can
# transiently fail with "Could not fetch rule set generation id: ... (you must
# be root)" — even though we ARE root with full capabilities (verified: CapEff
# all-ones, Seccomp 0). When that error hit the old detection `iptables -S`, the
# pipe came up empty, the loop concluded "no rules", and cleanup was skipped —
# stranding the panel + SSH ACCEPT rules after uninstall (reproduced
# intermittently on an arm64 host, never on x86). So: give nft a beat to settle,
# and run every iptables call through ipt_run(), which retries on the transient
# error and waits for the xtables lock (-w).
sleep 1

# ipt_run <iptables|ip6tables> <args...> — echo stdout, return the iptables rc.
# Retries up to ~3s on the transient nf_tables generation-id / xtables-lock /
# "temporarily unavailable" errors so a momentary hiccup never makes us skip a
# real rule. A genuine error (e.g. a -D for a rule that no longer exists →
# "Bad rule") is NOT in the retry set, so it returns immediately.
ipt_run() {
  local bin="$1"; shift
  local i out rc
  for i in $(seq 1 10); do
    out=$("$bin" -w 5 "$@" 2>&1); rc=$?
    [ "$rc" -eq 0 ] && { printf '%s\n' "$out"; return 0; }
    case "$out" in
      *"generation id"*|*"emporarily unavailable"*|*"xtables lock"*|*"you must be root"*)
        sleep 0.3; continue ;;
      *) return "$rc" ;;
    esac
  done
  return 1
}

ipt_changed=0
# Per-rule `-D` off the LIVE table (filter INPUT ACCEPTs + nat PREROUTING
# REDIRECTs), v4 and v6 — never a save→restore round-trip, which on nft-backed
# hosts can fail on an unrelated rule and silently no-op, leaving our rules
# behind. A `guard` counter bounds each table's loop so a genuinely undeletable
# rule can't spin forever; ipt_run's own retry absorbs transient failures.
for ipt in iptables ip6tables; do
  command -v "$ipt" >/dev/null 2>&1 || continue
  for tbl in filter nat; do
    guard=0
    while [ "$guard" -lt 100 ]; do
      guard=$((guard + 1))
      # Re-list each pass (ipt_run retries through transient nft errors); break
      # only when the listing is clean of our tag, never on a transient empty.
      listing=$(ipt_run "$ipt" -t "$tbl" -S) || break
      rule=$(printf '%s\n' "$listing" | grep 'edgenest-managed' | head -1)
      [ -n "$rule" ] || break
      # shellcheck disable=SC2086 — intentional word-split of the rule spec
      ipt_run "$ipt" -t "$tbl" $(printf '%s' "$rule" | sed 's/^-A /-D /') >/dev/null && ipt_changed=1
    done
  done
done
# Persist the cleaned ruleset, otherwise iptables-persistent restores our rules
# on the next boot from the stale /etc/iptables/rules.v{4,6} saved at install.
# Also re-persist when the saved files still carry our rules even if the live
# table was already clean (e.g. a prior uninstall removed live rules but didn't
# rewrite the saved file) — otherwise the next boot brings them back.
if [ "$ipt_changed" = "1" ] || grep -qs 'edgenest-managed' \
     /etc/iptables/rules.v4 /etc/iptables/rules.v6 /etc/sysconfig/iptables; then
  if command -v netfilter-persistent >/dev/null 2>&1; then
    netfilter-persistent save >/dev/null 2>&1 || true
  elif [ -d /etc/iptables ]; then
    iptables-save  > /etc/iptables/rules.v4 2>/dev/null || true
    ip6tables-save > /etc/iptables/rules.v6 2>/dev/null || true
  elif [ -f /etc/sysconfig/iptables ] && command -v iptables-save >/dev/null 2>&1; then
    # RHEL/Alma/Rocky persist path (mirror install.sh): rewrite the saved file
    # from the cleaned live table, else the next boot restores our rules.
    iptables-save  > /etc/sysconfig/iptables  2>/dev/null || true
    ip6tables-save > /etc/sysconfig/ip6tables 2>/dev/null || true
  fi
fi

# Restore /etc/resolv.conf if install.sh swapped it for Kasper DNS64 (v6-only
# host). We do this before any purge so the backup file is still readable.
resolv_backup="$DATA_DIR/resolv.conf.pre-edgenest"
if [ -f "$resolv_backup" ]; then
  cp -f "$resolv_backup" /etc/resolv.conf 2>/dev/null || true
  rm -f "$resolv_backup"
  logger -t edgenest-uninstall "restored /etc/resolv.conf from $resolv_backup" 2>/dev/null || true
fi

# Restore v6 sysctl if install.sh disabled it (v4-only host). Re-enables the
# kernel defaults so the OS can take v6 back the moment a v6 address shows up.
sysctl_v6_file="/etc/sysctl.d/99-edgenest-v6.conf"
if [ -f "$sysctl_v6_file" ]; then
  rm -f "$sysctl_v6_file"
  sysctl -w net.ipv6.conf.all.disable_ipv6=0 >/dev/null 2>&1 || true
  sysctl -w net.ipv6.conf.default.disable_ipv6=0 >/dev/null 2>&1 || true
  logger -t edgenest-uninstall "removed $sysctl_v6_file, v6 sysctl restored to kernel defaults" 2>/dev/null || true
fi

# Remove BBR sysctl + module-load files written by install.sh (revert to kernel
# defaults; harmless to leave but keeps the box clean).
rm -f /etc/sysctl.d/99-edgenest-bbr.conf /etc/modules-load.d/edgenest-bbr.conf 2>/dev/null || true

# Drop the Go-toolchain PATH line install.sh appended to /etc/profile during a
# source build (no-op if the install used the prebuilt Release path). Match the
# EXACT line install.sh wrote, not any line mentioning go/bin, so we never strip
# the operator's own Go PATH entry.
sed -i '\#export PATH=\$PATH:/usr/local/go/bin#d' /etc/profile 2>/dev/null || true

# Remove the 127.0.1.1 hostname line install.sh appended (tagged with a trailing
# marker so we delete exactly our line and never the operator's /etc/hosts rows).
sed -i '/# added by edgenest installer$/d' /etc/hosts 2>/dev/null || true

# Restore systemd's default NTP config if install.sh wrote its own timesyncd
# override (install overwrites without a backup, so removing our marked file
# lets systemd-timesyncd fall back to its compiled-in defaults).
if grep -qi 'Written by EdgeNest installer' /etc/systemd/timesyncd.conf 2>/dev/null; then
  rm -f /etc/systemd/timesyncd.conf
  systemctl restart systemd-timesyncd 2>/dev/null || true
fi

if [ "$PURGE" = "1" ]; then
  info "$(t step_purge "$DATA_DIR" "$LOG_DIR")"
  rm -rf "$DATA_DIR" "$LOG_DIR"
else
  # Make the keep-vs-purge distinction visible at a glance — 14d feedback was
  # "--purge feels the same as no --purge". Now keep-mode prints an explicit
  # "kept" line and purge-mode prints the actual paths being removed.
  yellow "$(t step_keep_note "$DATA_DIR" "$LOG_DIR")"
fi

echo ""
if [ "$PURGE" = "1" ]; then
  green "$(t done_purge)"
else
  green "$(t done_keep "$DATA_DIR" "$LOG_DIR")"
fi
yellow "$(t done_residual_hint)"
# Only mention journald when an OLD-style plaintext credential banner is actually
# present in the unit's journal — never nag a clean install about a leak that
# isn't there. "save these credentials" is the pre-1.03 banner header (printed to
# stdout → captured by journald); the current build logs only "credentials
# written to <path>" and never matches. journalctl resolves the unit by stored
# _SYSTEMD_UNIT records, so this still works after the unit file is removed above.
if command -v journalctl >/dev/null 2>&1 && \
   journalctl -u edgenest --no-pager 2>/dev/null | grep -q 'save these credentials'; then
  yellow "$(t done_journald_hint)"
fi
echo ""
