#!/usr/bin/env bash
#
# EdgeNest one-shot installer.
#
# Usage:
#   git clone https://github.com/aipo-lenshow/EdgeNest.git
#   cd EdgeNest
#   sudo bash scripts/install.sh
#
# The installer only asks for fields that the web panel cannot change after
# install (panel port) or that establish the public-facing URL (host). All
# protocol / client / route configuration happens in the web panel.
#
# Override via env vars: EDGENEST_VERSION, SINGBOX_VERSION, XRAY_VERSION,
# GO_VERSION, NODE_MAJOR, EDGENEST_RELEASE_BASE, EDGENEST_LANG.

set -euo pipefail

# 脚本里 web/, ./bin, ./cmd 等全是相对路径, 隐式依赖 cwd=repo root。
# 不管用户 `bash scripts/install.sh` (cwd=repo root) 还是
# `bash /path/to/scripts/install.sh` (cwd=别处), 都从 repo root 开始。
cd "$(dirname "${BASH_SOURCE[0]}")/.."

# ---- Flags ----
ASSUME_YES=0
NO_BBR=0
NO_PREBUILT=0
LANG_CHOICE=""   # filled by ask_language (en|zh|zh-TW|fa|ru|vi)
for arg in "$@"; do
  case "$arg" in
    --yes|-y)        ASSUME_YES=1 ;;
    --no-bbr)        NO_BBR=1 ;;
    --no-prebuilt)   NO_PREBUILT=1 ;;
    --lang=en|--lang=zh|--lang=zh-TW|--lang=fa|--lang=ru|--lang=vi) LANG_CHOICE="${arg#--lang=}" ;;
    -h|--help)
      cat <<'EOF'
EdgeNest installer

Usage:
  sudo bash scripts/install.sh            interactive (asks language first, then host / panel port / xray opt-in)
  sudo bash scripts/install.sh --yes      non-interactive (all defaults; language = $LANG-detected; host = public IP)
  sudo bash scripts/install.sh --lang=en  force a specific UI language (skip language prompt)
                                          accepted: en | zh | zh-TW | fa | ru | vi
  sudo bash scripts/install.sh --no-bbr   skip BBR sysctl tuning
  sudo bash scripts/install.sh --no-prebuilt
                                          force source build (skip GitHub Release prebuilt)

Env var overrides:
  EDGENEST_LANG          en | zh | zh-TW | fa | ru | vi (overrides $LANG-detected default; --lang= wins over this)
  EDGENEST_VERSION       (default 1.12.0624, used for prebuilt download URL)
  EDGENEST_RELEASE_BASE  (default https://github.com/aipo-lenshow/EdgeNest/releases/download)
  SINGBOX_VERSION        (default 1.13.13; always built/obtained with the with_v2ray_api tag)
  XRAY_VERSION           (default 26.3.27)
  GO_VERSION             (default 1.25.0, used if edgenest OR sing-box needs a source build + Go missing)
  NODE_MAJOR             (default 20, used only if edgenest source build needed + Node missing)
EOF
      exit 0 ;;
  esac
done

# ---- Pinned versions ----
EDGENEST_VERSION="${EDGENEST_VERSION:-1.12.0624}"
EDGENEST_RELEASE_BASE="${EDGENEST_RELEASE_BASE:-https://github.com/aipo-lenshow/EdgeNest/releases/download}"
SINGBOX_VERSION="${SINGBOX_VERSION:-1.13.13}"
XRAY_VERSION="${XRAY_VERSION:-26.3.27}"
GO_VERSION="${GO_VERSION:-1.26.0}"
NODE_MAJOR="${NODE_MAJOR:-20}"

# ---- Layout ----
INSTALL_BIN="/usr/local/bin"
DATA_DIR="/etc/edgenest"
LOG_DIR="/var/log/edgenest"
XRAY_SHARE_DIR="/usr/local/share/xray"
SYSTEMD_UNIT="/etc/systemd/system/edgenest.service"
BBR_SYSCTL_FILE="/etc/sysctl.d/99-edgenest-bbr.conf"

# ---- Defaults ----
DEFAULT_PANEL_PORT=2087
OLD_PANEL_PORT=""         # existing panel port on re-run/upgrade (BUGLOG 0-1); set in ask_user_config
INSTALL_SOURCE="source"   # "prebuilt" or "source" — filled by ensure_edgenest_binary
SINGBOX_SOURCE="source"   # "system"/"local"/"release"/"source" — filled by ensure_singbox_binary
BBR_RESULT="skipped"      # "enabled" / "already" / "skipped" / "unsupported" / "failed"
BBR_BEFORE=""
BBR_AFTER=""

# ---- Colors ----
if [ -t 1 ]; then
  C_RED='\033[31m'; C_GREEN='\033[32m'; C_YELLOW='\033[33m'
  C_CYAN='\033[36m'; C_BOLD='\033[1m'; C_DIM='\033[2m'; C_RESET='\033[0m'
else
  C_RED=''; C_GREEN=''; C_YELLOW=''; C_CYAN=''; C_BOLD=''; C_DIM=''; C_RESET=''
fi
red()    { printf "${C_RED}%s${C_RESET}\n" "$*"; }
green()  { printf "${C_GREEN}%s${C_RESET}\n" "$*"; }
yellow() { printf "${C_YELLOW}%s${C_RESET}\n" "$*"; }
info()   { printf "${C_CYAN}▶ %s${C_RESET}\n" "$*"; }
hint()   { printf "  ${C_DIM}%s${C_RESET}\n" "$*"; }

# ---------------------------------------------------------------------------
# i18n
# ---------------------------------------------------------------------------

declare -A I18N_EN I18N_ZH I18N_ZHTW I18N_FA I18N_RU I18N_VI

I18N_EN[need_root]="Please run as root (manages systemd/iptables/sysctl/low ports)."
I18N_ZH[need_root]="请用 root 运行 (要管 systemd / iptables / sysctl / 低端口)。"
I18N_ZHTW[need_root]="請用 root 執行 (要管 systemd / iptables / sysctl / 低連接埠)。"
I18N_FA[need_root]="لطفاً با کاربر root اجرا کنید (مدیریت systemd/iptables/sysctl/پورت‌های پایین)."
I18N_RU[need_root]="Запустите от root (управление systemd/iptables/sysctl/низкими портами)."
I18N_EN[need_root_hint]="  sudo bash scripts/install.sh"
I18N_ZH[need_root_hint]="  sudo bash scripts/install.sh"
I18N_ZHTW[need_root_hint]="  sudo bash scripts/install.sh"
I18N_FA[need_root_hint]="  sudo bash scripts/install.sh"
I18N_RU[need_root_hint]="  sudo bash scripts/install.sh"

I18N_EN[unsupported_os]="Unsupported OS: %s (supported: Debian/Ubuntu/CentOS/Alma/Rocky/Fedora)"
I18N_ZH[unsupported_os]="不支持的系统: %s (支持: Debian / Ubuntu / CentOS / Alma / Rocky / Fedora)"
I18N_ZHTW[unsupported_os]="不支援的系統: %s (支援: Debian / Ubuntu / CentOS / Alma / Rocky / Fedora)"
I18N_FA[unsupported_os]="سیستم‌عامل پشتیبانی‌نشده: %s (پشتیبانی‌شده: Debian/Ubuntu/CentOS/Alma/Rocky/Fedora)"
I18N_RU[unsupported_os]="Неподдерживаемая ОС: %s (поддерживаются: Debian/Ubuntu/CentOS/Alma/Rocky/Fedora)"
I18N_EN[unsupported_arch]="Unsupported arch: %s"
I18N_ZH[unsupported_arch]="不支持的 CPU 架构: %s"
I18N_ZHTW[unsupported_arch]="不支援的 CPU 架構: %s"
I18N_FA[unsupported_arch]="معماری پشتیبانی‌نشده: %s"
I18N_RU[unsupported_arch]="Неподдерживаемая архитектура: %s"

I18N_EN[lang_prompt]="Language / 语言"
I18N_ZH[lang_prompt]="Language / 语言"
I18N_ZHTW[lang_prompt]="Language / 语言"
I18N_FA[lang_prompt]="Language / 语言"
I18N_RU[lang_prompt]="Language / 语言"
I18N_EN[lang_options]="[1] English  [2] 中文  [3] 繁體中文  [4] فارسی  [5] Русский  [6] Tiếng Việt"
I18N_ZH[lang_options]="[1] English  [2] 中文  [3] 繁體中文  [4] فارسی  [5] Русский  [6] Tiếng Việt"
I18N_ZHTW[lang_options]="[1] English  [2] 中文  [3] 繁體中文  [4] فارسی  [5] Русский  [6] Tiếng Việt"
I18N_FA[lang_options]="[1] English  [2] 中文  [3] 繁體中文  [4] فارسی  [5] Русский  [6] Tiếng Việt"
I18N_RU[lang_options]="[1] English  [2] 中文  [3] 繁體中文  [4] فارسی  [5] Русский  [6] Tiếng Việt"
I18N_VI[lang_options]="[1] English  [2] 中文  [3] 繁體中文  [4] فارسی  [5] Русский  [6] Tiếng Việt"
I18N_EN[lang_set_en]="Language set to English."
I18N_ZH[lang_set_zh]="已切换到中文。"
I18N_ZHTW[lang_set_zhtw]="已切換到繁體中文。"
I18N_FA[lang_set_fa]="زبان روی فارسی تنظیم شد."
I18N_RU[lang_set_ru]="Язык установлен на русский."
I18N_VI[lang_set_vi]="Đã đặt ngôn ngữ thành Tiếng Việt."

I18N_EN[srv_header]="  ===== Server ====="
I18N_ZH[srv_header]="  ===== 服务器信息 ====="
I18N_ZHTW[srv_header]="  ===== 伺服器資訊 ====="
I18N_FA[srv_header]="  ===== سرور ====="
I18N_RU[srv_header]="  ===== Сервер ====="
I18N_EN[srv_os]="    OS       : %s"
I18N_ZH[srv_os]="    系统     : %s"
I18N_ZHTW[srv_os]="    系統     : %s"
I18N_FA[srv_os]="    سیستم    : %s"
I18N_RU[srv_os]="    ОС       : %s"
I18N_EN[srv_kernel]="    Kernel   : %s"
I18N_ZH[srv_kernel]="    内核     : %s"
I18N_ZHTW[srv_kernel]="    核心     : %s"
I18N_FA[srv_kernel]="    کرنل     : %s"
I18N_RU[srv_kernel]="    Ядро     : %s"
I18N_EN[srv_arch]="    Arch     : %s"
I18N_ZH[srv_arch]="    架构     : %s"
I18N_ZHTW[srv_arch]="    架構     : %s"
I18N_FA[srv_arch]="    معماری   : %s"
I18N_RU[srv_arch]="    Архит.   : %s"
I18N_EN[srv_cpu]="    CPU      : %s vCPU"
I18N_ZH[srv_cpu]="    CPU      : %s vCPU"
I18N_ZHTW[srv_cpu]="    CPU      : %s vCPU"
I18N_FA[srv_cpu]="    CPU      : %s vCPU"
I18N_RU[srv_cpu]="    CPU      : %s vCPU"
I18N_EN[srv_cpu_full]="    CPU      : %s vCPU  (%s physical core × %s thread/core)"
I18N_ZH[srv_cpu_full]="    CPU      : %s vCPU  (%s 物理核 × 每核 %s 线程)"
I18N_ZHTW[srv_cpu_full]="    CPU      : %s vCPU  (%s 實體核 × 每核 %s 執行緒)"
I18N_FA[srv_cpu_full]="    CPU      : %s vCPU  (%s هسته فیزیکی × %s رشته/هسته)"
I18N_RU[srv_cpu_full]="    CPU      : %s vCPU  (%s физ. ядро × %s поток/ядро)"
I18N_EN[srv_cpu_model]="    CPU 型号 : %s"
I18N_ZH[srv_cpu_model]="    CPU 型号 : %s"
I18N_ZHTW[srv_cpu_model]="    CPU 型號 : %s"
I18N_FA[srv_cpu_model]="    مدل CPU  : %s"
I18N_RU[srv_cpu_model]="    Модель CPU : %s"
I18N_EN[srv_mem]="    Memory   : %s"
I18N_ZH[srv_mem]="    内存     : %s"
I18N_ZHTW[srv_mem]="    記憶體   : %s"
I18N_FA[srv_mem]="    حافظه    : %s"
I18N_RU[srv_mem]="    Память   : %s"
I18N_EN[srv_ip]="    Public IP: %s"
I18N_ZH[srv_ip]="    公网 IP  : %s"
I18N_ZHTW[srv_ip]="    公網 IP  : %s"
I18N_FA[srv_ip]="    IP عمومی : %s"
I18N_RU[srv_ip]="    Публ. IP : %s"
I18N_EN[srv_ip_unknown]="unknown"
I18N_ZH[srv_ip_unknown]="未知"
I18N_ZHTW[srv_ip_unknown]="未知"
I18N_FA[srv_ip_unknown]="نامشخص"
I18N_RU[srv_ip_unknown]="неизвестно"

I18N_EN[setup_header]="===== EdgeNest Setup ====="
I18N_ZH[setup_header]="===== EdgeNest 安装设置 ====="
I18N_ZHTW[setup_header]="===== EdgeNest 安裝設定 ====="
I18N_FA[setup_header]="===== نصب EdgeNest ====="
I18N_RU[setup_header]="===== Установка EdgeNest ====="
I18N_EN[setup_intro]="Please answer the prompts below. Advanced configuration happens in the web panel after install."
I18N_ZH[setup_intro]="请回答以下问题。装好后所有进阶配置都在 web 面板里改。"
I18N_ZHTW[setup_intro]="請回答以下問題。裝好後所有進階設定都在 web 面板裡改。"
I18N_FA[setup_intro]="لطفاً به سؤالات زیر پاسخ دهید. پیکربندی پیشرفته پس از نصب در پنل وب انجام می‌شود."
I18N_RU[setup_intro]="Ответьте на вопросы ниже. Расширенная настройка выполняется в веб-панели после установки."

I18N_EN[host_hint]="host = your server's public address. Use a domain if you've set DNS, otherwise the server IP. Editable later in the web panel."
I18N_ZH[host_hint]="host = 服务器对外地址。配过域名就用域名, 否则用服务器 IP。装好后能在 web 面板里改。"
I18N_ZHTW[host_hint]="host = 伺服器對外位址。設過網域就用網域, 否則用伺服器 IP。裝好後能在 web 面板裡改。"
I18N_FA[host_hint]="host = آدرس عمومی سرور شما. اگر DNS تنظیم کرده‌اید از دامنه استفاده کنید، در غیر این صورت IP سرور. بعداً در پنل وب قابل ویرایش است."
I18N_RU[host_hint]="host = публичный адрес вашего сервера. Используйте домен, если настроили DNS, иначе IP сервера. Позже можно изменить в веб-панели."
I18N_EN[host_label]="host"
I18N_ZH[host_label]="host"
I18N_ZHTW[host_label]="host"
I18N_FA[host_label]="host"
I18N_RU[host_label]="host"
I18N_EN[port_hint]="Web panel port (TCP)"
I18N_ZH[port_hint]="Web 面板端口 (TCP)"
I18N_ZHTW[port_hint]="Web 面板連接埠 (TCP)"
I18N_FA[port_hint]="پورت پنل وب (TCP)"
I18N_RU[port_hint]="Порт веб-панели (TCP)"
I18N_EN[port_label]="panel port"
I18N_ZH[port_label]="面板端口"
I18N_ZHTW[port_label]="面板連接埠"
I18N_FA[port_label]="پورت پنل"
I18N_RU[port_label]="порт панели"
I18N_EN[port_preserve]="Existing install detected — keeping current panel port tcp/%s (override below)."
I18N_ZH[port_preserve]="检测到已有安装 — 沿用当前面板端口 tcp/%s (可在下方覆盖)。"
I18N_ZHTW[port_preserve]="偵測到已有安裝 — 沿用目前面板連接埠 tcp/%s (可在下方覆寫)。"
I18N_FA[port_preserve]="نصب موجود شناسایی شد — پورت فعلی پنل tcp/%s حفظ می‌شود (در زیر قابل تغییر)."
I18N_RU[port_preserve]="Обнаружена существующая установка — сохраняется текущий порт панели tcp/%s (можно изменить ниже)."

I18N_EN[xray_hint1]="Default install ships sing-box protocols (VLESS-Reality / Hysteria2 / Trojan / SS-2022 / TUIC / VMess-WS / VLESS-WS)."
I18N_ZH[xray_hint1]="默认装的 sing-box 自带协议: VLESS-Reality / Hysteria2 / Trojan / SS-2022 / TUIC / VMess-WS / VLESS-WS。"
I18N_ZHTW[xray_hint1]="預設安裝的 sing-box 自帶協議: VLESS-Reality / Hysteria2 / Trojan / SS-2022 / TUIC / VMess-WS / VLESS-WS。"
I18N_FA[xray_hint1]="نصب پیش‌فرض پروتکل‌های sing-box را شامل می‌شود (VLESS-Reality / Hysteria2 / Trojan / SS-2022 / TUIC / VMess-WS / VLESS-WS)."
I18N_RU[xray_hint1]="Установка по умолчанию включает протоколы sing-box (VLESS-Reality / Hysteria2 / Trojan / SS-2022 / TUIC / VMess-WS / VLESS-WS)."
I18N_EN[xray_hint2]="Pick 'y' if you also want xray-core protocols (VLESS-XHTTP-Reality / XHTTP-ENC / AnyTLS)."
I18N_ZH[xray_hint2]="如果还要 xray-core 才支持的协议 (VLESS-XHTTP-Reality / XHTTP-ENC / AnyTLS), 选 y。"
I18N_ZHTW[xray_hint2]="如果還要 xray-core 才支援的協議 (VLESS-XHTTP-Reality / XHTTP-ENC / AnyTLS), 選 y。"
I18N_FA[xray_hint2]="اگر پروتکل‌های xray-core را هم می‌خواهید (VLESS-XHTTP-Reality / XHTTP-ENC / AnyTLS)، 'y' را انتخاب کنید."
I18N_RU[xray_hint2]="Выберите 'y', если вам также нужны протоколы xray-core (VLESS-XHTTP-Reality / XHTTP-ENC / AnyTLS)."
I18N_EN[xray_label]="install xray-core v%s"
I18N_ZH[xray_label]="安装 xray-core v%s"
I18N_ZHTW[xray_label]="安裝 xray-core v%s"
I18N_FA[xray_label]="نصب xray-core نسخه %s"
I18N_RU[xray_label]="установить xray-core v%s"

I18N_EN[confirm_header]="===== About to install ====="
I18N_ZH[confirm_header]="===== 即将安装 ====="
I18N_ZHTW[confirm_header]="===== 即將安裝 ====="
I18N_FA[confirm_header]="===== در آستانه نصب ====="
I18N_RU[confirm_header]="===== Готово к установке ====="
I18N_EN[confirm_host]="  host         : %s"
I18N_ZH[confirm_host]="  host         : %s"
I18N_ZHTW[confirm_host]="  host         : %s"
I18N_FA[confirm_host]="  host         : %s"
I18N_RU[confirm_host]="  host         : %s"
I18N_EN[confirm_port]="  panel port   : %s (TCP)"
I18N_ZH[confirm_port]="  面板端口     : %s (TCP)"
I18N_ZHTW[confirm_port]="  面板連接埠   : %s (TCP)"
I18N_FA[confirm_port]="  پورت پنل     : %s (TCP)"
I18N_RU[confirm_port]="  порт панели  : %s (TCP)"
I18N_EN[confirm_lang]="  Panel language : %s   (panel language same)"
I18N_ZH[confirm_lang]="  面板语言       : %s   (面板语言相同)"
I18N_ZHTW[confirm_lang]="  面板語言       : %s   (面板語言相同)"
I18N_FA[confirm_lang]="  زبان پنل       : %s   (زبان پنل یکسان)"
I18N_RU[confirm_lang]="  Язык панели    : %s   (язык панели тот же)"
I18N_EN[confirm_xray_yes]="  xray-core    : yes (v%s)"
I18N_ZH[confirm_xray_yes]="  xray-core    : 是 (v%s)"
I18N_ZHTW[confirm_xray_yes]="  xray-core    : 是 (v%s)"
I18N_FA[confirm_xray_yes]="  xray-core    : بله (v%s)"
I18N_RU[confirm_xray_yes]="  xray-core    : да (v%s)"
I18N_EN[confirm_xray_no]="  xray-core    : no"
I18N_ZH[confirm_xray_no]="  xray-core    : 否"
I18N_ZHTW[confirm_xray_no]="  xray-core    : 否"
I18N_FA[confirm_xray_no]="  xray-core    : خیر"
I18N_RU[confirm_xray_no]="  xray-core    : нет"
I18N_EN[confirm_bbr_skip]="  BBR tuning   : skip (--no-bbr)"
I18N_ZH[confirm_bbr_skip]="  BBR 调优     : 跳过 (--no-bbr)"
I18N_ZHTW[confirm_bbr_skip]="  BBR 調優     : 跳過 (--no-bbr)"
I18N_FA[confirm_bbr_skip]="  تنظیم BBR    : رد شد (--no-bbr)"
I18N_RU[confirm_bbr_skip]="  Настройка BBR : пропуск (--no-bbr)"
I18N_EN[confirm_bbr_on]="  BBR tuning   : enable (silent)"
I18N_ZH[confirm_bbr_on]="  BBR 调优     : 开启 (静默)"
I18N_ZHTW[confirm_bbr_on]="  BBR 調優     : 開啟 (靜默)"
I18N_FA[confirm_bbr_on]="  تنظیم BBR    : فعال (بی‌صدا)"
I18N_RU[confirm_bbr_on]="  Настройка BBR : включить (тихо)"
I18N_EN[confirm_components]="  Components that will be installed:"
I18N_ZH[confirm_components]="  将安装以下组件:"
I18N_ZHTW[confirm_components]="  將安裝以下元件:"
I18N_FA[confirm_components]="  اجزایی که نصب خواهند شد:"
I18N_RU[confirm_components]="  Будут установлены компоненты:"
I18N_EN[confirm_pkgs]="    - System packages: curl, git, tar, unzip, ca-certificates, sqlite3, iptables, python3"
I18N_ZH[confirm_pkgs]="    - 系统包: curl, git, tar, unzip, ca-certificates, sqlite3, iptables, python3"
I18N_ZHTW[confirm_pkgs]="    - 系統套件: curl, git, tar, unzip, ca-certificates, sqlite3, iptables, python3"
I18N_FA[confirm_pkgs]="    - بسته‌های سیستمی: curl, git, tar, unzip, ca-certificates, sqlite3, iptables, python3"
I18N_RU[confirm_pkgs]="    - Системные пакеты: curl, git, tar, unzip, ca-certificates, sqlite3, iptables, python3"
I18N_EN[confirm_singbox]="    - sing-box v%s"
I18N_ZH[confirm_singbox]="    - sing-box v%s"
I18N_ZHTW[confirm_singbox]="    - sing-box v%s"
I18N_FA[confirm_singbox]="    - sing-box v%s"
I18N_RU[confirm_singbox]="    - sing-box v%s"
I18N_EN[confirm_xray_comp]="    - xray-core v%s"
I18N_ZH[confirm_xray_comp]="    - xray-core v%s"
I18N_ZHTW[confirm_xray_comp]="    - xray-core v%s"
I18N_FA[confirm_xray_comp]="    - xray-core v%s"
I18N_RU[confirm_xray_comp]="    - xray-core v%s"
I18N_EN[confirm_edgenest]="    - edgenest v%s"
I18N_ZH[confirm_edgenest]="    - edgenest v%s"
I18N_ZHTW[confirm_edgenest]="    - edgenest v%s"
I18N_FA[confirm_edgenest]="    - edgenest v%s"
I18N_RU[confirm_edgenest]="    - edgenest v%s"
I18N_EN[confirm_systemd]="    - systemd unit (auto-start on boot)"
I18N_ZH[confirm_systemd]="    - systemd 服务单元 (开机自启)"
I18N_ZHTW[confirm_systemd]="    - systemd 服務單元 (開機自動啟動)"
I18N_FA[confirm_systemd]="    - واحد systemd (راه‌اندازی خودکار هنگام بوت)"
I18N_RU[confirm_systemd]="    - юнит systemd (автозапуск при загрузке)"
I18N_EN[confirm_iptables]="    - iptables rule for panel port + iptables-persistent"
I18N_ZH[confirm_iptables]="    - iptables 放行面板端口 + iptables-persistent 持久化"
I18N_ZHTW[confirm_iptables]="    - iptables 放行面板連接埠 + iptables-persistent 持久化"
I18N_FA[confirm_iptables]="    - قانون iptables برای پورت پنل + iptables-persistent"
I18N_RU[confirm_iptables]="    - правило iptables для порта панели + iptables-persistent"
I18N_EN[confirm_bbr_line]="    - BBR + fq sysctl (kernel TCP tuning)"
I18N_ZH[confirm_bbr_line]="    - BBR + fq sysctl (内核 TCP 调优)"
I18N_ZHTW[confirm_bbr_line]="    - BBR + fq sysctl (核心 TCP 調優)"
I18N_FA[confirm_bbr_line]="    - BBR + fq sysctl (تنظیم TCP کرنل)"
I18N_RU[confirm_bbr_line]="    - BBR + fq sysctl (настройка TCP ядра)"
I18N_EN[confirm_ntp_line]="    - NTP (system clock sync, required by SS-2022 / Hysteria2 / TUIC)"
I18N_ZH[confirm_ntp_line]="    - NTP (系统时间同步, SS-2022 / Hysteria2 / TUIC 协议要求)"
I18N_ZHTW[confirm_ntp_line]="    - NTP (系統時間同步, SS-2022 / Hysteria2 / TUIC 協議要求)"
I18N_FA[confirm_ntp_line]="    - NTP (همگام‌سازی ساعت سیستم، مورد نیاز SS-2022 / Hysteria2 / TUIC)"
I18N_RU[confirm_ntp_line]="    - NTP (синхронизация системных часов, требуется для SS-2022 / Hysteria2 / TUIC)"
I18N_EN[ntp_syncing]="Synchronizing system clock (SS-2022 / Hy2 reject clients > 30s apart)…"
I18N_ZH[ntp_syncing]="同步系统时间 (SS-2022 / Hy2 协议会拒绝时间差 >30s 的客户端)…"
I18N_ZHTW[ntp_syncing]="同步系統時間 (SS-2022 / Hy2 協議會拒絕時間差 >30s 的用戶端)…"
I18N_FA[ntp_syncing]="در حال همگام‌سازی ساعت سیستم (SS-2022 / Hy2 کلاینت‌های با اختلاف بیش از ۳۰ ثانیه را رد می‌کنند)…"
I18N_RU[ntp_syncing]="Синхронизация системных часов (SS-2022 / Hy2 отклоняют клиентов с расхождением > 30с)…"
I18N_EN[ntp_synced]="Time synchronized: drift %ss vs RTC"
I18N_ZH[ntp_synced]="时间已同步: 跟 RTC 偏差 %s 秒"
I18N_ZHTW[ntp_synced]="時間已同步: 跟 RTC 偏差 %s 秒"
I18N_FA[ntp_synced]="زمان همگام شد: انحراف %s ثانیه نسبت به RTC"
I18N_RU[ntp_synced]="Время синхронизировано: расхождение %sс относительно RTC"
I18N_EN[ntp_skewed]="WARNING: clock skew vs RTC = %ss. SS-2022 / Hy2 may reject some clients."
I18N_ZH[ntp_skewed]="警告: 跟 RTC 偏差 %s 秒, SS-2022 / Hy2 可能拒绝部分客户端。"
I18N_ZHTW[ntp_skewed]="警告: 跟 RTC 偏差 %s 秒, SS-2022 / Hy2 可能拒絕部分用戶端。"
I18N_FA[ntp_skewed]="هشدار: انحراف ساعت نسبت به RTC = %s ثانیه. ممکن است SS-2022 / Hy2 برخی کلاینت‌ها را رد کنند."
I18N_RU[ntp_skewed]="ВНИМАНИЕ: расхождение часов относительно RTC = %sс. SS-2022 / Hy2 могут отклонять некоторых клиентов."
I18N_EN[confirm_go]="confirm and start"
I18N_ZH[confirm_go]="确认开始安装"
I18N_ZHTW[confirm_go]="確認開始安裝"
I18N_FA[confirm_go]="تأیید و شروع"
I18N_RU[confirm_go]="подтвердить и начать"
I18N_EN[cancelled]="Cancelled."
I18N_ZH[cancelled]="已取消。"
I18N_ZHTW[cancelled]="已取消。"
I18N_FA[cancelled]="لغو شد."
I18N_RU[cancelled]="Отменено."

I18N_EN[ask_default]="[default: %s]"
I18N_ZH[ask_default]="[默认: %s]"
I18N_ZHTW[ask_default]="[預設: %s]"
I18N_FA[ask_default]="[پیش‌فرض: %s]"
I18N_RU[ask_default]="[по умолчанию: %s]"

I18N_EN[deps_installing]="Installing system packages (curl / unzip / sqlite3 / iptables / python3)…"
I18N_ZH[deps_installing]="安装系统依赖包 (curl / unzip / sqlite3 / iptables / python3)…"
I18N_ZHTW[deps_installing]="安裝系統相依套件 (curl / unzip / sqlite3 / iptables / python3)…"
I18N_FA[deps_installing]="در حال نصب بسته‌های سیستمی (curl / unzip / sqlite3 / iptables / python3)…"
I18N_RU[deps_installing]="Установка системных пакетов (curl / unzip / sqlite3 / iptables / python3)…"

I18N_EN[bin_local_found]="Found local ./bin/edgenest, using it (skip prebuilt + skip source build)."
I18N_ZH[bin_local_found]="发现本地 ./bin/edgenest, 直接用 (跳过预编译下载 + 跳过源码构建)。"
I18N_ZHTW[bin_local_found]="發現本地 ./bin/edgenest, 直接用 (跳過預編譯下載 + 跳過原始碼建置)。"
I18N_FA[bin_local_found]="فایل محلی ./bin/edgenest پیدا شد، از آن استفاده می‌شود (رد کردن دانلود از پیش ساخته‌شده + رد کردن ساخت از منبع)."
I18N_RU[bin_local_found]="Найден локальный ./bin/edgenest, используется он (пропуск готовой сборки + пропуск сборки из исходников)."
I18N_EN[bin_prebuilt_fallback]="Prebuilt release not available; falling back to source build."
I18N_ZH[bin_prebuilt_fallback]="预编译产物不可用, 回退到源码构建。"
I18N_ZHTW[bin_prebuilt_fallback]="預編譯產物不可用, 回退到原始碼建置。"
I18N_FA[bin_prebuilt_fallback]="نسخه از پیش ساخته‌شده در دسترس نیست؛ به ساخت از منبع بازمی‌گردیم."
I18N_RU[bin_prebuilt_fallback]="Готовый релиз недоступен; переход к сборке из исходников."
I18N_EN[bin_no_prebuilt]="--no-prebuilt: skipping prebuilt release download."
I18N_ZH[bin_no_prebuilt]="--no-prebuilt: 跳过预编译下载, 直接源码构建。"
I18N_ZHTW[bin_no_prebuilt]="--no-prebuilt: 跳過預編譯下載, 直接原始碼建置。"
I18N_FA[bin_no_prebuilt]="--no-prebuilt: دانلود نسخه از پیش ساخته‌شده رد می‌شود."
I18N_RU[bin_no_prebuilt]="--no-prebuilt: пропуск загрузки готового релиза."
I18N_EN[prebuilt_trying]="Trying GitHub Release prebuilt: %s / linux-%s…"
I18N_ZH[prebuilt_trying]="尝试 GitHub Release 预编译: %s / linux-%s…"
I18N_ZHTW[prebuilt_trying]="嘗試 GitHub Release 預編譯: %s / linux-%s…"
I18N_FA[prebuilt_trying]="در حال تلاش برای نسخه از پیش ساخته‌شده GitHub Release: %s / linux-%s…"
I18N_RU[prebuilt_trying]="Попытка готовой сборки GitHub Release: %s / linux-%s…"
I18N_EN[prebuilt_ok]="Prebuilt binary installed: ./bin/edgenest (%s)"
I18N_ZH[prebuilt_ok]="预编译二进制就位: ./bin/edgenest (%s)"
I18N_ZHTW[prebuilt_ok]="預編譯二進位檔就位: ./bin/edgenest (%s)"
I18N_FA[prebuilt_ok]="باینری از پیش ساخته‌شده نصب شد: ./bin/edgenest (%s)"
I18N_RU[prebuilt_ok]="Готовый бинарник установлен: ./bin/edgenest (%s)"

I18N_EN[go_installing]="Installing Go %s…"
I18N_ZH[go_installing]="安装 Go %s…"
I18N_ZHTW[go_installing]="安裝 Go %s…"
I18N_FA[go_installing]="در حال نصب Go %s…"
I18N_RU[go_installing]="Установка Go %s…"
I18N_EN[go_installed]="Go installed: %s"
I18N_ZH[go_installed]="Go 已装: %s"
I18N_ZHTW[go_installed]="Go 已裝: %s"
I18N_FA[go_installed]="Go نصب شد: %s"
I18N_RU[go_installed]="Go установлен: %s"
I18N_EN[node_installing]="Installing Node %s.x…"
I18N_ZH[node_installing]="安装 Node %s.x…"
I18N_ZHTW[node_installing]="安裝 Node %s.x…"
I18N_FA[node_installing]="در حال نصب Node %s.x…"
I18N_RU[node_installing]="Установка Node %s.x…"
I18N_EN[node_installed]="Node installed: %s + npm %s"
I18N_ZH[node_installed]="Node 已装: %s + npm %s"
I18N_ZHTW[node_installed]="Node 已裝: %s + npm %s"
I18N_FA[node_installed]="Node نصب شد: %s + npm %s"
I18N_RU[node_installed]="Node установлен: %s + npm %s"

I18N_EN[build_web]="Building front-end SPA (npm ci + build)…"
I18N_ZH[build_web]="构建前端 SPA (npm ci + npm run build)…"
I18N_ZHTW[build_web]="建置前端 SPA (npm ci + npm run build)…"
I18N_FA[build_web]="در حال ساخت SPA فرانت‌اند (npm ci + build)…"
I18N_RU[build_web]="Сборка фронтенд-SPA (npm ci + build)…"
I18N_EN[build_sync]="Syncing SPA to embed dir (internal/control/web/dist)…"
I18N_ZH[build_sync]="同步 SPA 到嵌入目录 (internal/control/web/dist)…"
I18N_ZHTW[build_sync]="同步 SPA 到嵌入目錄 (internal/control/web/dist)…"
I18N_FA[build_sync]="در حال همگام‌سازی SPA با پوشه embed (internal/control/web/dist)…"
I18N_RU[build_sync]="Синхронизация SPA в каталог встраивания (internal/control/web/dist)…"
I18N_EN[build_go]="Building edgenest binary (go build, embeds SPA)…"
I18N_ZH[build_go]="构建 edgenest 二进制 (go build, 嵌入 SPA)…"
I18N_ZHTW[build_go]="建置 edgenest 二進位檔 (go build, 嵌入 SPA)…"
I18N_FA[build_go]="در حال ساخت باینری edgenest (go build، SPA را تعبیه می‌کند)…"
I18N_RU[build_go]="Сборка бинарника edgenest (go build, встраивает SPA)…"
I18N_EN[build_done]="Build complete: ./bin/edgenest (%s)"
I18N_ZH[build_done]="构建完成: ./bin/edgenest (%s)"
I18N_ZHTW[build_done]="建置完成: ./bin/edgenest (%s)"
I18N_FA[build_done]="ساخت کامل شد: ./bin/edgenest (%s)"
I18N_RU[build_done]="Сборка завершена: ./bin/edgenest (%s)"

I18N_EN[sb_system_ok]="System sing-box matches pinned version + v2ray_api, reusing: %s"
I18N_ZH[sb_system_ok]="系统已有 sing-box 版本与 v2ray_api 均匹配, 直接复用: %s"
I18N_ZHTW[sb_system_ok]="系統已有 sing-box 版本與 v2ray_api 均符合, 直接重用: %s"
I18N_FA[sb_system_ok]="sing-box سیستم با نسخه ثابت‌شده + v2ray_api مطابقت دارد، استفاده مجدد: %s"
I18N_RU[sb_system_ok]="Системный sing-box соответствует закреплённой версии + v2ray_api, повторное использование: %s"
I18N_EN[sb_local_found]="Found local custom sing-box (with_v2ray_api), using it (skip download + build)."
I18N_ZH[sb_local_found]="发现本地自编译 sing-box (含 with_v2ray_api), 直接用 (跳过下载 + 跳过构建)。"
I18N_ZHTW[sb_local_found]="發現本地自行編譯的 sing-box (含 with_v2ray_api), 直接用 (跳過下載 + 跳過建置)。"
I18N_FA[sb_local_found]="sing-box سفارشی محلی پیدا شد (with_v2ray_api)، از آن استفاده می‌شود (رد کردن دانلود + ساخت)."
I18N_RU[sb_local_found]="Найден локальный кастомный sing-box (with_v2ray_api), используется он (пропуск загрузки + сборки)."
I18N_EN[sb_release_trying]="Trying EdgeNest Release sing-box (with_v2ray_api): v%s / linux-%s…"
I18N_ZH[sb_release_trying]="尝试 EdgeNest Release 自编译 sing-box (含 with_v2ray_api): v%s / linux-%s…"
I18N_ZHTW[sb_release_trying]="嘗試 EdgeNest Release 自行編譯的 sing-box (含 with_v2ray_api): v%s / linux-%s…"
I18N_FA[sb_release_trying]="در حال تلاش برای sing-box از EdgeNest Release (with_v2ray_api): v%s / linux-%s…"
I18N_RU[sb_release_trying]="Попытка sing-box из EdgeNest Release (with_v2ray_api): v%s / linux-%s…"
I18N_EN[sb_release_fallback]="Release sing-box not available; building from source (scripts/build-singbox.sh)."
I18N_ZH[sb_release_fallback]="Release 自编译 sing-box 不可用, 改用源码构建 (scripts/build-singbox.sh)。"
I18N_ZHTW[sb_release_fallback]="Release 自行編譯的 sing-box 不可用, 改用原始碼建置 (scripts/build-singbox.sh)。"
I18N_FA[sb_release_fallback]="sing-box نسخه Release در دسترس نیست؛ ساخت از منبع (scripts/build-singbox.sh)."
I18N_RU[sb_release_fallback]="Release sing-box недоступен; сборка из исходников (scripts/build-singbox.sh)."
I18N_EN[sb_building]="Building sing-box v%s with with_v2ray_api (per-user quota needs it; first build takes a few minutes)…"
I18N_ZH[sb_building]="正在编译 sing-box v%s (含 with_v2ray_api, 流量配额必需; 首次编译要几分钟)…"
I18N_ZHTW[sb_building]="正在編譯 sing-box v%s (含 with_v2ray_api, 流量配額必需; 首次編譯要幾分鐘)…"
I18N_FA[sb_building]="در حال ساخت sing-box v%s با with_v2ray_api (سهمیه به‌ازای کاربر به آن نیاز دارد؛ اولین ساخت چند دقیقه طول می‌کشد)…"
I18N_RU[sb_building]="Сборка sing-box v%s с with_v2ray_api (нужно для квоты на пользователя; первая сборка занимает несколько минут)…"
I18N_EN[sb_built]="sing-box built from source: %s"
I18N_ZH[sb_built]="sing-box 源码构建完成: %s"
I18N_ZHTW[sb_built]="sing-box 原始碼建置完成: %s"
I18N_FA[sb_built]="sing-box از منبع ساخته شد: %s"
I18N_RU[sb_built]="sing-box собран из исходников: %s"
I18N_EN[sb_installed]="sing-box installed: %s"
I18N_ZH[sb_installed]="sing-box 已装: %s"
I18N_ZHTW[sb_installed]="sing-box 已裝: %s"
I18N_FA[sb_installed]="sing-box نصب شد: %s"
I18N_RU[sb_installed]="sing-box установлен: %s"
I18N_EN[sb_fatal]="FATAL: could not obtain a sing-box with with_v2ray_api (required for per-user traffic quota). Aborting rather than install a crippled engine."
I18N_ZH[sb_fatal]="致命错误: 无法获得带 with_v2ray_api 的 sing-box (流量配额必需)。已中止, 不安装功能残缺的引擎。"
I18N_ZHTW[sb_fatal]="致命錯誤: 無法取得帶 with_v2ray_api 的 sing-box (流量配額必需)。已中止, 不安裝功能殘缺的引擎。"
I18N_FA[sb_fatal]="خطای مهلک: امکان به‌دست‌آوردن sing-box با with_v2ray_api فراهم نشد (برای سهمیه ترافیک به‌ازای کاربر لازم است). به‌جای نصب موتور ناقص، لغو شد."
I18N_RU[sb_fatal]="КРИТИЧЕСКАЯ ОШИБКА: не удалось получить sing-box с with_v2ray_api (нужно для квоты трафика на пользователя). Прерывание вместо установки урезанного движка."
I18N_EN[xray_skip]="Skip xray-core."
I18N_ZH[xray_skip]="跳过 xray-core。"
I18N_ZHTW[xray_skip]="跳過 xray-core。"
I18N_FA[xray_skip]="رد کردن xray-core."
I18N_RU[xray_skip]="Пропуск xray-core."
I18N_EN[xray_present]="xray already present at pinned version, skipping."
I18N_ZH[xray_present]="xray 已是指定版本, 跳过。"
I18N_ZHTW[xray_present]="xray 已是指定版本, 跳過。"
I18N_FA[xray_present]="xray از قبل با نسخه ثابت‌شده موجود است، رد می‌شود."
I18N_RU[xray_present]="xray уже присутствует в закреплённой версии, пропуск."
I18N_EN[xray_version_mismatch]="System xray v%s != pinned v%s; reinstalling pinned version."
I18N_ZH[xray_version_mismatch]="系统 xray v%s 与指定 v%s 不符, 重装指定版本。"
I18N_ZHTW[xray_version_mismatch]="系統 xray v%s 與指定 v%s 不符, 重新安裝指定版本。"
I18N_FA[xray_version_mismatch]="xray سیستم v%s با نسخه ثابت‌شده v%s مطابقت ندارد؛ نسخه ثابت‌شده دوباره نصب می‌شود."
I18N_RU[xray_version_mismatch]="Системный xray v%s != закреплённая v%s; переустановка закреплённой версии."
I18N_EN[xray_unsupported]="xray unsupported arch: %s"
I18N_ZH[xray_unsupported]="xray 不支持该架构: %s"
I18N_ZHTW[xray_unsupported]="xray 不支援該架構: %s"
I18N_FA[xray_unsupported]="معماری پشتیبانی‌نشده توسط xray: %s"
I18N_RU[xray_unsupported]="xray не поддерживает архитектуру: %s"
I18N_EN[xray_downloading]="Downloading xray-core v%s (%s)…"
I18N_ZH[xray_downloading]="下载 xray-core v%s (%s)…"
I18N_ZHTW[xray_downloading]="下載 xray-core v%s (%s)…"
I18N_FA[xray_downloading]="در حال دانلود xray-core v%s (%s)…"
I18N_RU[xray_downloading]="Загрузка xray-core v%s (%s)…"
I18N_EN[xray_installed]="xray installed: %s"
I18N_ZH[xray_installed]="xray 已装: %s"
I18N_ZHTW[xray_installed]="xray 已裝: %s"
I18N_FA[xray_installed]="xray نصب شد: %s"
I18N_RU[xray_installed]="xray установлен: %s"
I18N_EN[xray_verify_fail]="FATAL: expected xray v%s but installed binary reports v%s. Aborting."
I18N_ZH[xray_verify_fail]="致命错误: 期望 xray v%s, 实际装出的是 v%s。已中止。"
I18N_ZHTW[xray_verify_fail]="致命錯誤: 期望 xray v%s, 實際裝出的是 v%s。已中止。"
I18N_FA[xray_verify_fail]="خطای مهلک: انتظار xray v%s می‌رفت اما باینری نصب‌شده v%s را گزارش می‌دهد. لغو شد."
I18N_RU[xray_verify_fail]="КРИТИЧЕСКАЯ ОШИБКА: ожидался xray v%s, но установленный бинарник сообщает v%s. Прерывание."

I18N_EN[edgenest_installing]="Installing EdgeNest binary…"
I18N_ZH[edgenest_installing]="安装 EdgeNest 二进制…"
I18N_ZHTW[edgenest_installing]="安裝 EdgeNest 二進位檔…"
I18N_FA[edgenest_installing]="در حال نصب باینری EdgeNest…"
I18N_RU[edgenest_installing]="Установка бинарника EdgeNest…"
I18N_EN[edgenest_missing]="./bin/edgenest missing — build failed?"
I18N_ZH[edgenest_missing]="./bin/edgenest 不存在 — 构建失败了？"
I18N_ZHTW[edgenest_missing]="./bin/edgenest 不存在 — 建置失敗了？"
I18N_FA[edgenest_missing]="./bin/edgenest وجود ندارد — ساخت ناموفق بود؟"
I18N_RU[edgenest_missing]="./bin/edgenest отсутствует — сборка не удалась?"
I18N_EN[edgenest_installed]="Installed: %s"
I18N_ZH[edgenest_installed]="已安装: %s"
I18N_ZHTW[edgenest_installed]="已安裝: %s"
I18N_FA[edgenest_installed]="نصب شد: %s"
I18N_RU[edgenest_installed]="Установлено: %s"
I18N_EN[setup_dirs]="Creating data dirs (panel port = %s)…"
I18N_ZH[setup_dirs]="创建数据目录 (面板端口 = %s)…"
I18N_ZHTW[setup_dirs]="建立資料目錄 (面板連接埠 = %s)…"
I18N_FA[setup_dirs]="در حال ایجاد پوشه‌های داده (پورت پنل = %s)…"
I18N_RU[setup_dirs]="Создание каталогов данных (порт панели = %s)…"
I18N_EN[cap_v4only]="Tuned network settings for this IPv4-only node."
I18N_ZH[cap_v4only]="已为纯 IPv4 节点优化网络设置。"
I18N_ZHTW[cap_v4only]="已為純 IPv4 節點最佳化網路設定。"
I18N_FA[cap_v4only]="تنظیمات شبکه برای این گره فقط-IPv4 بهینه شد."
I18N_RU[cap_v4only]="Настройки сети оптимизированы для этого узла только-IPv4."
I18N_EN[cap_v6only]="Configured DNS64 resolver for this IPv6-only node."
I18N_ZH[cap_v6only]="已为纯 IPv6 节点配置 DNS64 解析。"
I18N_ZHTW[cap_v6only]="已為純 IPv6 節點設定 DNS64 解析。"
I18N_FA[cap_v6only]="حل‌کننده DNS64 برای این گره فقط-IPv6 پیکربندی شد."
I18N_RU[cap_v6only]="Настроен резолвер DNS64 для этого узла только-IPv6."

I18N_EN[fw_opening]="Opening panel port in iptables…"
I18N_ZH[fw_opening]="iptables 放行面板端口…"
I18N_ZHTW[fw_opening]="iptables 放行面板連接埠…"
I18N_FA[fw_opening]="در حال باز کردن پورت پنل در iptables…"
I18N_RU[fw_opening]="Открытие порта панели в iptables…"
I18N_EN[fw_skip]="tcp/%s already open, skip"
I18N_ZH[fw_skip]="tcp/%s 已放行, 跳过"
I18N_ZHTW[fw_skip]="tcp/%s 已放行, 跳過"
I18N_FA[fw_skip]="tcp/%s از قبل باز است، رد می‌شود"
I18N_RU[fw_skip]="tcp/%s уже открыт, пропуск"
I18N_EN[fw_opened]="tcp/%s opened"
I18N_ZH[fw_opened]="tcp/%s 已放行"
I18N_ZHTW[fw_opened]="tcp/%s 已放行"
I18N_FA[fw_opened]="tcp/%s باز شد"
I18N_RU[fw_opened]="tcp/%s открыт"
I18N_EN[fw_old_cleaned]="port changed %s→%s — removed stale iptables rule for old tcp/%s"
I18N_ZH[fw_old_cleaned]="端口已变 %s→%s — 清理旧 tcp/%s 的遗留 iptables 规则"
I18N_ZHTW[fw_old_cleaned]="連接埠已變 %s→%s — 清理舊 tcp/%s 的遺留 iptables 規則"
I18N_FA[fw_old_cleaned]="پورت تغییر کرد %s→%s — قانون قدیمی iptables برای tcp/%s قبلی حذف شد"
I18N_RU[fw_old_cleaned]="порт изменён %s→%s — удалено устаревшее правило iptables для старого tcp/%s"
I18N_EN[fw_done]="iptables configured + persisted (panel only — protocol ports open on demand from the web panel)"
I18N_ZH[fw_done]="iptables 已配置 + 持久化 (只放行面板; 协议端口装入站时按需放行)"
I18N_ZHTW[fw_done]="iptables 已設定 + 持久化 (只放行面板; 協議連接埠建入站時按需放行)"
I18N_FA[fw_done]="iptables پیکربندی و پایدار شد (فقط پنل — پورت‌های پروتکل بر حسب نیاز از پنل وب باز می‌شوند)"
I18N_RU[fw_done]="iptables настроен + сохранён (только панель — порты протоколов открываются по запросу из веб-панели)"

I18N_EN[bbr_skip]="--no-bbr: skipping BBR sysctl tuning."
I18N_ZH[bbr_skip]="--no-bbr: 跳过 BBR sysctl 调优。"
I18N_ZHTW[bbr_skip]="--no-bbr: 跳過 BBR sysctl 調優。"
I18N_FA[bbr_skip]="--no-bbr: تنظیم sysctl برای BBR رد می‌شود."
I18N_RU[bbr_skip]="--no-bbr: пропуск настройки BBR sysctl."
I18N_EN[bbr_enabling]="Enabling BBR + fq (TCP congestion control + qdisc)…"
I18N_ZH[bbr_enabling]="启用 BBR + fq (TCP 拥塞控制 + qdisc)…"
I18N_ZHTW[bbr_enabling]="啟用 BBR + fq (TCP 壅塞控制 + qdisc)…"
I18N_FA[bbr_enabling]="در حال فعال‌سازی BBR + fq (کنترل ازدحام TCP + qdisc)…"
I18N_RU[bbr_enabling]="Включение BBR + fq (управление перегрузкой TCP + qdisc)…"
I18N_EN[bbr_already]="Already on BBR, leaving sysctl as-is."
I18N_ZH[bbr_already]="已经是 BBR, sysctl 保持不动。"
I18N_ZHTW[bbr_already]="已經是 BBR, sysctl 保持不動。"
I18N_FA[bbr_already]="هم‌اکنون روی BBR است، sysctl بدون تغییر می‌ماند."
I18N_RU[bbr_already]="Уже на BBR, sysctl оставлен без изменений."
I18N_EN[bbr_unsupported]="Kernel has no tcp_bbr module. Leaving congestion control = %s."
I18N_ZH[bbr_unsupported]="内核没有 tcp_bbr 模块。拥塞控制保持 = %s。"
I18N_ZHTW[bbr_unsupported]="核心沒有 tcp_bbr 模組。壅塞控制保持 = %s。"
I18N_FA[bbr_unsupported]="کرنل ماژول tcp_bbr ندارد. کنترل ازدحام = %s باقی می‌ماند."
I18N_RU[bbr_unsupported]="В ядре нет модуля tcp_bbr. Управление перегрузкой остаётся = %s."
I18N_EN[bbr_enabled]="BBR enabled (persisted via %s)"
I18N_ZH[bbr_enabled]="BBR 已启用 (持久化到 %s)"
I18N_ZHTW[bbr_enabled]="BBR 已啟用 (持久化到 %s)"
I18N_FA[bbr_enabled]="BBR فعال شد (از طریق %s پایدار شد)"
I18N_RU[bbr_enabled]="BBR включён (сохранено через %s)"
I18N_EN[bbr_failed]="sysctl reports congestion control = %s (expected bbr)."
I18N_ZH[bbr_failed]="sysctl 报告拥塞控制 = %s (本期望 bbr)。"
I18N_ZHTW[bbr_failed]="sysctl 報告壅塞控制 = %s (本期望 bbr)。"
I18N_FA[bbr_failed]="sysctl کنترل ازدحام = %s را گزارش می‌دهد (انتظار bbr می‌رفت)."
I18N_RU[bbr_failed]="sysctl сообщает управление перегрузкой = %s (ожидался bbr)."

I18N_EN[svc_starting]="Starting edgenest service…"
I18N_ZH[svc_starting]="启动 edgenest 服务…"
I18N_ZHTW[svc_starting]="啟動 edgenest 服務…"
I18N_FA[svc_starting]="در حال راه‌اندازی سرویس edgenest…"
I18N_RU[svc_starting]="Запуск службы edgenest…"
I18N_EN[svc_timeout]="edgenest didn't come up within 30s, logs:"
I18N_ZH[svc_timeout]="edgenest 30 秒内未启动, 日志:"
I18N_ZHTW[svc_timeout]="edgenest 30 秒內未啟動, 日誌:"
I18N_FA[svc_timeout]="edgenest در عرض ۳۰ ثانیه بالا نیامد، لاگ‌ها:"
I18N_RU[svc_timeout]="edgenest не запустился за 30с, логи:"
I18N_EN[svc_timeout_cred]="If the service starts shortly, first-run credentials will be at %s/first-run.cred (sudo cat to read once; or run: sudo edgenest reset-pass)."
I18N_ZH[svc_timeout_cred]="若服务稍后启动, 首次凭据将位于 %s/first-run.cred (sudo cat 读取一次; 或运行: sudo edgenest reset-pass)。"
I18N_ZHTW[svc_timeout_cred]="若服務稍後啟動, 首次憑據將位於 %s/first-run.cred (sudo cat 讀取一次; 或執行: sudo edgenest reset-pass)。"
I18N_FA[svc_timeout_cred]="اگر سرویس کمی بعد بالا بیاید، اعتبارنامهٔ اولین اجرا در %s/first-run.cred خواهد بود (با sudo cat یک‌بار بخوانید؛ یا اجرا کنید: sudo edgenest reset-pass)."
I18N_RU[svc_timeout_cred]="Если служба запустится чуть позже, учётные данные первого запуска будут в %s/first-run.cred (sudo cat для разового чтения; или: sudo edgenest reset-pass)."
I18N_EN[svc_path_unrecoverable]="Could not recover the panel path from the database — run 'sudo edgenest status' to see the panel URL."
I18N_ZH[svc_path_unrecoverable]="无法从数据库恢复面板路径 — 运行 'sudo edgenest status' 查看面板地址。"
I18N_ZHTW[svc_path_unrecoverable]="無法從資料庫復原面板路徑 — 執行 'sudo edgenest status' 查看面板地址。"
I18N_FA[svc_path_unrecoverable]="مسیر پنل از پایگاه‌داده بازیابی نشد — برای دیدن آدرس پنل 'sudo edgenest status' را اجرا کنید."
I18N_RU[svc_path_unrecoverable]="Не удалось восстановить путь панели из базы данных — выполните 'sudo edgenest status', чтобы увидеть URL панели."
I18N_EN[svc_db_existed]="Existing database detected — reusing your previous account / inbounds / certs (login details in the summary below)."
I18N_ZH[svc_db_existed]="检测到已保留的数据库 — 复用原有账号 / 入站 / 证书 (登录信息见下方摘要)。"
I18N_ZHTW[svc_db_existed]="偵測到已保留的資料庫 — 沿用原有帳號 / 入站 / 憑證 (登入資訊見下方摘要)。"
I18N_FA[svc_db_existed]="پایگاه‌دادهٔ موجود شناسایی شد — حساب / ورودی‌ها / گواهی‌های قبلی شما دوباره استفاده می‌شود (جزئیات ورود در خلاصهٔ پایین)."
I18N_RU[svc_db_existed]="Обнаружена сохранённая база данных — используются ваши прежние учётная запись / входящие / сертификаты (данные для входа — в сводке ниже)."
I18N_EN[svc_pwd_preserved]="(unchanged — old admin password preserved)"
I18N_ZH[svc_pwd_preserved]="(未变 — 沿用原管理员密码)"
I18N_ZHTW[svc_pwd_preserved]="(未變 — 沿用原管理員密碼)"
I18N_FA[svc_pwd_preserved]="(بدون تغییر — رمز عبور قدیمی مدیر حفظ شد)"
I18N_RU[svc_pwd_preserved]="(без изменений — старый пароль администратора сохранён)"

I18N_EN[sum_title]="            ✅  EdgeNest installed successfully"
I18N_ZH[sum_title]="            ✅  EdgeNest 安装成功"
I18N_ZHTW[sum_title]="            ✅  EdgeNest 安裝成功"
I18N_FA[sum_title]="            ✅  EdgeNest با موفقیت نصب شد"
I18N_RU[sum_title]="            ✅  EdgeNest успешно установлен"
I18N_EN[sum_svc]="📦 Service status"
I18N_ZH[sum_svc]="📦 服务状态"
I18N_ZHTW[sum_svc]="📦 服務狀態"
I18N_FA[sum_svc]="📦 وضعیت سرویس"
I18N_RU[sum_svc]="📦 Состояние службы"
I18N_EN[sum_svc_edgenest]="    edgenest   : v%s  (%s, auto-start enabled)"
I18N_ZH[sum_svc_edgenest]="    edgenest   : v%s  (%s, 已设置开机自启)"
I18N_ZHTW[sum_svc_edgenest]="    edgenest   : v%s  (%s, 已設定開機自動啟動)"
I18N_FA[sum_svc_edgenest]="    edgenest   : v%s  (%s، راه‌اندازی خودکار فعال)"
I18N_RU[sum_svc_edgenest]="    edgenest   : v%s  (%s, автозапуск включён)"
I18N_EN[svc_state_active]="running"
I18N_ZH[svc_state_active]="运行"
I18N_ZHTW[svc_state_active]="執行中"
I18N_FA[svc_state_active]="در حال اجرا"
I18N_RU[svc_state_active]="работает"
I18N_EN[sum_svc_singbox]="    sing-box   : v%s    (engine, %s)"
I18N_ZH[sum_svc_singbox]="    sing-box   : v%s    (引擎, %s)"
I18N_ZHTW[sum_svc_singbox]="    sing-box   : v%s    (引擎, %s)"
I18N_FA[sum_svc_singbox]="    sing-box   : v%s    (موتور، %s)"
I18N_RU[sum_svc_singbox]="    sing-box   : v%s    (движок, %s)"
I18N_EN[sb_src_system]="reused system binary"
I18N_ZH[sb_src_system]="复用系统二进制"
I18N_ZHTW[sb_src_system]="重用系統二進位檔"
I18N_FA[sb_src_system]="باینری سیستمی استفاده مجدد شد"
I18N_RU[sb_src_system]="повторно использован системный бинарник"
I18N_EN[sb_src_local]="local prebuilt"
I18N_ZH[sb_src_local]="本地预编译"
I18N_ZHTW[sb_src_local]="本地預編譯"
I18N_FA[sb_src_local]="از پیش ساخته‌شده محلی"
I18N_RU[sb_src_local]="локальная готовая сборка"
I18N_EN[sb_src_release]="release download"
I18N_ZH[sb_src_release]="Release 下载"
I18N_ZHTW[sb_src_release]="Release 下載"
I18N_FA[sb_src_release]="دانلود از release"
I18N_RU[sb_src_release]="загрузка релиза"
I18N_EN[sb_src_source]="built from source"
I18N_ZH[sb_src_source]="源码编译"
I18N_ZHTW[sb_src_source]="原始碼編譯"
I18N_FA[sb_src_source]="ساخته‌شده از منبع"
I18N_RU[sb_src_source]="собрано из исходников"
I18N_EN[sum_svc_xray_yes]="    xray-core  : v%s    (engine, optional protocols)"
I18N_ZH[sum_svc_xray_yes]="    xray-core  : v%s    (引擎, 可选协议)"
I18N_ZHTW[sum_svc_xray_yes]="    xray-core  : v%s    (引擎, 選用協議)"
I18N_FA[sum_svc_xray_yes]="    xray-core  : v%s    (موتور، پروتکل‌های اختیاری)"
I18N_RU[sum_svc_xray_yes]="    xray-core  : v%s    (движок, опциональные протоколы)"
I18N_EN[sum_svc_xray_no]="    xray-core  : not installed   (re-run installer to add)"
I18N_ZH[sum_svc_xray_no]="    xray-core  : 未安装   (重跑 install.sh 可加装)"
I18N_ZHTW[sum_svc_xray_no]="    xray-core  : 未安裝   (重跑 install.sh 可加裝)"
I18N_FA[sum_svc_xray_no]="    xray-core  : نصب نشده   (برای افزودن، نصب‌کننده را دوباره اجرا کنید)"
I18N_RU[sum_svc_xray_no]="    xray-core  : не установлен   (перезапустите установщик для добавления)"
I18N_EN[sum_svc_binsrc]="    binary src : %s"
I18N_ZH[sum_svc_binsrc]="    二进制来源 : %s"
I18N_ZHTW[sum_svc_binsrc]="    二進位來源 : %s"
I18N_FA[sum_svc_binsrc]="    منبع باینری : %s"
I18N_RU[sum_svc_binsrc]="    источник бинарника : %s"
I18N_EN[sum_bbr_enabled]="    BBR        : ✅ enabled (sysctl %s → bbr, persisted)"
I18N_ZH[sum_bbr_enabled]="    BBR        : ✅ 已启用 (sysctl %s → bbr, 已持久化)"
I18N_ZHTW[sum_bbr_enabled]="    BBR        : ✅ 已啟用 (sysctl %s → bbr, 已持久化)"
I18N_FA[sum_bbr_enabled]="    BBR        : ✅ فعال شد (sysctl %s → bbr، پایدار شد)"
I18N_RU[sum_bbr_enabled]="    BBR        : ✅ включён (sysctl %s → bbr, сохранено)"
I18N_EN[sum_bbr_already]="    BBR        : ✅ already enabled (kernel %s)"
I18N_ZH[sum_bbr_already]="    BBR        : ✅ 已是 BBR (内核报告 %s)"
I18N_ZHTW[sum_bbr_already]="    BBR        : ✅ 已是 BBR (核心報告 %s)"
I18N_FA[sum_bbr_already]="    BBR        : ✅ از قبل فعال است (کرنل %s)"
I18N_RU[sum_bbr_already]="    BBR        : ✅ уже включён (ядро %s)"
I18N_EN[sum_bbr_skipped]="    BBR        : skipped (--no-bbr)"
I18N_ZH[sum_bbr_skipped]="    BBR        : 跳过 (--no-bbr)"
I18N_ZHTW[sum_bbr_skipped]="    BBR        : 跳過 (--no-bbr)"
I18N_FA[sum_bbr_skipped]="    BBR        : رد شد (--no-bbr)"
I18N_RU[sum_bbr_skipped]="    BBR        : пропущено (--no-bbr)"
I18N_EN[sum_bbr_unsupported]="    BBR        : ⚠️ kernel has no tcp_bbr module (current = %s)"
I18N_ZH[sum_bbr_unsupported]="    BBR        : ⚠️ 内核没有 tcp_bbr 模块 (当前 = %s)"
I18N_ZHTW[sum_bbr_unsupported]="    BBR        : ⚠️ 核心沒有 tcp_bbr 模組 (目前 = %s)"
I18N_FA[sum_bbr_unsupported]="    BBR        : ⚠️ کرنل ماژول tcp_bbr ندارد (فعلی = %s)"
I18N_RU[sum_bbr_unsupported]="    BBR        : ⚠️ в ядре нет модуля tcp_bbr (текущее = %s)"
I18N_EN[sum_bbr_failed]="    BBR        : ⚠️ sysctl set but kernel reports %s"
I18N_ZH[sum_bbr_failed]="    BBR        : ⚠️ sysctl 已设但内核报告 %s"
I18N_ZHTW[sum_bbr_failed]="    BBR        : ⚠️ sysctl 已設但核心報告 %s"
I18N_FA[sum_bbr_failed]="    BBR        : ⚠️ sysctl تنظیم شد اما کرنل %s را گزارش می‌دهد"
I18N_RU[sum_bbr_failed]="    BBR        : ⚠️ sysctl задан, но ядро сообщает %s"

I18N_EN[sum_login]="🌐 Panel login"
I18N_ZH[sum_login]="🌐 面板登录"
I18N_ZHTW[sum_login]="🌐 面板登入"
I18N_FA[sum_login]="🌐 ورود به پنل"
I18N_RU[sum_login]="🌐 Вход в панель"
I18N_EN[sum_url]="    URL        : %s"
I18N_ZH[sum_url]="    地址       : %s"
I18N_ZHTW[sum_url]="    位址       : %s"
I18N_FA[sum_url]="    آدرس      : %s"
I18N_RU[sum_url]="    URL        : %s"
I18N_EN[sum_url_alt]="    Alt (v6)   : %s"
I18N_ZH[sum_url_alt]="    备用 (v6)  : %s"
I18N_ZHTW[sum_url_alt]="    備用 (v6)  : %s"
I18N_FA[sum_url_alt]="    جایگزین (v6) : %s"
I18N_RU[sum_url_alt]="    Альт. (v6) : %s"
I18N_EN[sum_url_dual_note]="    Both addresses reach the same panel. The self-signed cert SANs both."
I18N_ZH[sum_url_dual_note]="    两个地址访问同一个面板, 自签 cert SAN 同时覆盖。"
I18N_ZHTW[sum_url_dual_note]="    兩個位址存取同一個面板, 自簽 cert SAN 同時涵蓋。"
I18N_FA[sum_url_dual_note]="    هر دو آدرس به یک پنل می‌رسند. گواهی self-signed هر دو را در SAN دارد."
I18N_RU[sum_url_dual_note]="    Оба адреса ведут к одной панели. Самоподписанный сертификат покрывает оба в SAN."
I18N_EN[sum_user]="    Username   : %s"
I18N_ZH[sum_user]="    用户名     : %s"
I18N_ZHTW[sum_user]="    使用者名稱 : %s"
I18N_FA[sum_user]="    نام کاربری : %s"
I18N_RU[sum_user]="    Логин      : %s"
I18N_EN[sum_pwd]="    Password   : %s"
I18N_ZH[sum_pwd]="    密码       : %s"
I18N_ZHTW[sum_pwd]="    密碼       : %s"
I18N_FA[sum_pwd]="    رمز عبور   : %s"
I18N_RU[sum_pwd]="    Пароль     : %s"
I18N_EN[sum_pwd_warn]="    ⚠️ First login forces a password change. Save the password above NOW (it won't be shown again)."
I18N_ZH[sum_pwd_warn]="    ⚠️ 首次登录会强制改密码。请立即记下上面的密码 (之后不会再显示)。"
I18N_ZHTW[sum_pwd_warn]="    ⚠️ 首次登入會強制改密碼。請立即記下上面的密碼 (之後不會再顯示)。"
I18N_FA[sum_pwd_warn]="    ⚠️ اولین ورود تغییر رمز عبور را الزامی می‌کند. همین حالا رمز عبور بالا را ذخیره کنید (دوباره نمایش داده نمی‌شود)."
I18N_RU[sum_pwd_warn]="    ⚠️ Первый вход требует смены пароля. Сохраните пароль выше СЕЙЧАС (он больше не будет показан)."

I18N_EN[host_detected_dual]="    Detected: IPv4 %s   +   IPv6 %s   (dual-stack — both can reach the panel)"
I18N_ZH[host_detected_dual]="    检测到: IPv4 %s   +   IPv6 %s   (双栈 — 两个都能访问面板)"
I18N_ZHTW[host_detected_dual]="    偵測到: IPv4 %s   +   IPv6 %s   (雙堆疊 — 兩個都能存取面板)"
I18N_FA[host_detected_dual]="    شناسایی شد: IPv4 %s   +   IPv6 %s   (دو-پشته — هر دو به پنل دسترسی دارند)"
I18N_RU[host_detected_dual]="    Обнаружено: IPv4 %s   +   IPv6 %s   (двойной стек — оба ведут к панели)"
I18N_EN[host_detected_v4only]="    Detected: IPv4 %s   (single-stack IPv4)"
I18N_ZH[host_detected_v4only]="    检测到: IPv4 %s   (单栈 IPv4)"
I18N_ZHTW[host_detected_v4only]="    偵測到: IPv4 %s   (單堆疊 IPv4)"
I18N_FA[host_detected_v4only]="    شناسایی شد: IPv4 %s   (تک-پشته IPv4)"
I18N_RU[host_detected_v4only]="    Обнаружено: IPv4 %s   (одинарный стек IPv4)"
I18N_EN[host_detected_v6only]="    Detected: IPv6 %s   (single-stack IPv6)"
I18N_ZH[host_detected_v6only]="    检测到: IPv6 %s   (单栈 IPv6)"
I18N_ZHTW[host_detected_v6only]="    偵測到: IPv6 %s   (單堆疊 IPv6)"
I18N_FA[host_detected_v6only]="    شناسایی شد: IPv6 %s   (تک-پشته IPv6)"
I18N_RU[host_detected_v6only]="    Обнаружено: IPv6 %s   (одинарный стек IPv6)"

I18N_EN[sum_capability]="🌍 Detected node capability"
I18N_ZH[sum_capability]="🌍 节点出口检测"
I18N_ZHTW[sum_capability]="🌍 節點出口偵測"
I18N_FA[sum_capability]="🌍 قابلیت شناسایی‌شده گره"
I18N_RU[sum_capability]="🌍 Обнаруженные возможности узла"
I18N_EN[sum_capability_v4]="    IPv4       : %s"
I18N_ZH[sum_capability_v4]="    IPv4       : %s"
I18N_ZHTW[sum_capability_v4]="    IPv4       : %s"
I18N_FA[sum_capability_v4]="    IPv4       : %s"
I18N_RU[sum_capability_v4]="    IPv4       : %s"
I18N_EN[sum_capability_v6]="    IPv6       : %s"
I18N_ZH[sum_capability_v6]="    IPv6       : %s"
I18N_ZHTW[sum_capability_v6]="    IPv6       : %s"
I18N_FA[sum_capability_v6]="    IPv6       : %s"
I18N_RU[sum_capability_v6]="    IPv6       : %s"
I18N_EN[sum_capability_none]="(not reachable)"
I18N_ZH[sum_capability_none]="(不可达)"
I18N_ZHTW[sum_capability_none]="(不可達)"
I18N_FA[sum_capability_none]="(در دسترس نیست)"
I18N_RU[sum_capability_none]="(недоступно)"
I18N_EN[sum_capability_dual_note]="    → Dual-stack: subscription URLs default to v4; in the wizard you can flip to v6 or run the wizard twice for two parallel subscriptions on the same server."
I18N_ZH[sum_capability_dual_note]="    → 双栈节点: 订阅 URL 默认走 v4, 创建入站向导第一屏可切到 v6; 也可以跑两次向导, 同一台服务器生成 v4 + v6 两套订阅, 客户端任切。"
I18N_ZHTW[sum_capability_dual_note]="    → 雙堆疊節點: 訂閱 URL 預設走 v4, 建立入站精靈第一頁可切到 v6; 也可以跑兩次精靈, 同一台伺服器產生 v4 + v6 兩套訂閱, 用戶端任切。"
I18N_FA[sum_capability_dual_note]="    → دو-پشته: URLهای اشتراک به‌طور پیش‌فرض از v4 استفاده می‌کنند؛ در ویزارد می‌توانید به v6 تغییر دهید یا ویزارد را دو بار اجرا کنید تا دو اشتراک موازی روی یک سرور داشته باشید."
I18N_RU[sum_capability_dual_note]="    → Двойной стек: URL подписок по умолчанию используют v4; в мастере можно переключиться на v6 или запустить мастер дважды для двух параллельных подписок на одном сервере."
I18N_EN[sum_capability_v4only_note]="    → IPv4-only node: subscription URLs use this address."
I18N_ZH[sum_capability_v4only_note]="    → 单 v4 节点: 订阅 URL 自动用这个地址。"
I18N_ZHTW[sum_capability_v4only_note]="    → 單 v4 節點: 訂閱 URL 自動用這個位址。"
I18N_FA[sum_capability_v4only_note]="    → گره فقط-IPv4: URLهای اشتراک از این آدرس استفاده می‌کنند."
I18N_RU[sum_capability_v4only_note]="    → Узел только-IPv4: URL подписок используют этот адрес."
I18N_EN[sum_capability_v6only_note]="    → IPv6-only node: subscription URLs use this address; Kasper DNS64 wired in /etc/resolv.conf for v4-only origins."
I18N_ZH[sum_capability_v6only_note]="    → 单 v6 节点: 订阅 URL 自动用这个地址; /etc/resolv.conf 已配 Kasper DNS64 透明走 NAT64 访问 v4-only 目标。"
I18N_ZHTW[sum_capability_v6only_note]="    → 單 v6 節點: 訂閱 URL 自動用這個位址; /etc/resolv.conf 已設 Kasper DNS64 透明走 NAT64 存取 v4-only 目標。"
I18N_FA[sum_capability_v6only_note]="    → گره فقط-IPv6: URLهای اشتراک از این آدرس استفاده می‌کنند؛ Kasper DNS64 در /etc/resolv.conf برای مقاصد فقط-v4 تنظیم شده است."
I18N_RU[sum_capability_v6only_note]="    → Узел только-IPv6: URL подписок используют этот адрес; Kasper DNS64 прописан в /etc/resolv.conf для назначений только-v4."
I18N_EN[sum_capability_neither_note]="    → Neither family is reachable — check your VPS network before creating inbounds."
I18N_ZH[sum_capability_neither_note]="    → 两个 family 都探测不到出口 — 创建入站之前先查一下 VPS 网络。"
I18N_ZHTW[sum_capability_neither_note]="    → 兩個 family 都偵測不到出口 — 建立入站之前先查一下 VPS 網路。"
I18N_FA[sum_capability_neither_note]="    → هیچ‌کدام از خانواده‌ها در دسترس نیست — قبل از ایجاد inbound شبکه VPS خود را بررسی کنید."
I18N_RU[sum_capability_neither_note]="    → Ни одно семейство недоступно — проверьте сеть VPS перед созданием входящих."

I18N_EN[sum_next]="👉 Next step"
I18N_ZH[sum_next]="👉 下一步"
I18N_ZHTW[sum_next]="👉 下一步"
I18N_FA[sum_next]="👉 گام بعدی"
I18N_RU[sum_next]="👉 Следующий шаг"
I18N_EN[sum_next_body]="    Log in → \"Create inbound wizard\" in the sidebar. The first card picks\n    the subscription host (IPv4 / IPv6); a single-stack node is auto-locked,\n    a dual-stack node lets you choose. Then pick a mode:\n      • Quick   — one click for 1-4 popular protocols\n      • Scenario — pick by use case (travel / family / max stealth …)\n      • Full    — domain validation + CDN / Argo"
I18N_ZH[sum_next_body]="    登录后到侧边栏 \"创建入站向导\"。首屏的卡片选订阅主机 (IPv4 / IPv6) —\n    单栈节点自动锁定; 双栈节点可任选。然后选模式:\n      • 快速创建   — 4 个常用协议挑 1-4 个一键起\n      • 按场景推荐 — 出差 / 全家用 / 极致隐蔽 等场景\n      • 完整向导   — 域名校验 + CDN / Argo 隧道"
I18N_ZHTW[sum_next_body]="    登入後到側邊欄 \"建立入站精靈\"。首頁的卡片選訂閱主機 (IPv4 / IPv6) —\n    單堆疊節點自動鎖定;\n    雙堆疊節點可任選。然後選模式:\n      • 快速建立   — 4 個常用協議挑 1-4 個一鍵起\n      • 按場景推薦 — 出差 / 全家用 / 極致隱蔽 等場景\n      • 完整精靈   — 網域校驗 + CDN / Argo 隧道"
I18N_FA[sum_next_body]="    وارد شوید → \"ویزارد ایجاد inbound\" در نوار کناری. کارت اول میزبان\n    اشتراک (IPv4 / IPv6) را انتخاب می‌کند؛ گره تک-پشته خودکار قفل می‌شود،\n    گره دو-پشته به شما اجازه انتخاب می‌دهد. سپس یک حالت انتخاب کنید:\n      • سریع    — یک کلیک برای ۱ تا ۴ پروتکل محبوب\n      • سناریو  — بر اساس کاربرد (سفر / خانواده / حداکثر پنهان‌کاری …)\n      • کامل    — اعتبارسنجی دامنه + CDN / Argo"
I18N_RU[sum_next_body]="    Войдите → \"Мастер создания входящих\" в боковой панели. Первая карточка\n    выбирает хост подписки (IPv4 / IPv6); узел с одинарным стеком блокируется\n    автоматически, узел с двойным стеком даёт выбор. Затем выберите режим:\n      • Быстрый  — один клик для 1-4 популярных протоколов\n      • Сценарий — по сценарию (поездка / семья / макс. скрытность …)\n      • Полный   — проверка домена + CDN / Argo"
I18N_EN[sum_next_dual_tip]="    Tip: dual-stack nodes can run the wizard twice — once for IPv4, once for IPv6 — to get two parallel subscriptions on the same server."
I18N_ZH[sum_next_dual_tip]="    提示: 双栈节点可以跑两次向导 (一次 IPv4, 一次 IPv6), 同一台服务器生成两套互相独立的订阅。"
I18N_ZHTW[sum_next_dual_tip]="    提示: 雙堆疊節點可以跑兩次精靈 (一次 IPv4, 一次 IPv6), 同一台伺服器產生兩套互相獨立的訂閱。"
I18N_FA[sum_next_dual_tip]="    نکته: گره‌های دو-پشته می‌توانند ویزارد را دو بار اجرا کنند — یک‌بار برای IPv4، یک‌بار برای IPv6 — تا دو اشتراک موازی روی یک سرور داشته باشند."
I18N_RU[sum_next_dual_tip]="    Совет: узлы с двойным стеком могут запустить мастер дважды — один раз для IPv4, один раз для IPv6 — чтобы получить две параллельные подписки на одном сервере."

I18N_EN[sum_fw]="🔥 Firewall"
I18N_ZH[sum_fw]="🔥 防火墙"
I18N_ZHTW[sum_fw]="🔥 防火牆"
I18N_FA[sum_fw]="🔥 فایروال"
I18N_RU[sum_fw]="🔥 Брандмауэр"
I18N_EN[sum_fw_local]="    ✅ Local iptables: panel port (tcp/%s) opened + persisted"
I18N_ZH[sum_fw_local]="    ✅ 本机 iptables: 面板端口 (tcp/%s) 已放行 + 持久化"
I18N_ZHTW[sum_fw_local]="    ✅ 本機 iptables: 面板連接埠 (tcp/%s) 已放行 + 持久化"
I18N_FA[sum_fw_local]="    ✅ iptables محلی: پورت پنل (tcp/%s) باز و پایدار شد"
I18N_RU[sum_fw_local]="    ✅ Локальный iptables: порт панели (tcp/%s) открыт + сохранён"
I18N_EN[sum_fw_hint]="       Protocol ports open automatically when you create inbounds in the web panel."
I18N_ZH[sum_fw_hint]="       协议端口会在你 web 面板里建入站时自动放行。"
I18N_ZHTW[sum_fw_hint]="       協議連接埠會在你 web 面板裡建入站時自動放行。"
I18N_FA[sum_fw_hint]="       پورت‌های پروتکل هنگام ایجاد inbound در پنل وب به‌طور خودکار باز می‌شوند."
I18N_RU[sum_fw_hint]="       Порты протоколов открываются автоматически при создании входящих в веб-панели."
I18N_EN[sum_fw_cloud]="    ⚠️ Cloud security groups (Oracle / AWS / GCP / Azure / Vultr / DigitalOcean) must be opened separately!"
I18N_ZH[sum_fw_cloud]="    ⚠️ 云厂商安全组 (Oracle / AWS / GCP / Azure / Vultr / DigitalOcean 等) 需要另外去控制台放行！"
I18N_ZHTW[sum_fw_cloud]="    ⚠️ 雲端廠商安全群組 (Oracle / AWS / GCP / Azure / Vultr / DigitalOcean 等) 需要另外去主控台放行！"
I18N_FA[sum_fw_cloud]="    ⚠️ گروه‌های امنیتی ابری (Oracle / AWS / GCP / Azure / Vultr / DigitalOcean) باید جداگانه باز شوند!"
I18N_RU[sum_fw_cloud]="    ⚠️ Облачные группы безопасности (Oracle / AWS / GCP / Azure / Vultr / DigitalOcean) нужно открывать отдельно!"
I18N_EN[sum_fw_ingress]="       Required ingress: tcp/%s (panel) + whatever ports your inbounds use."
I18N_ZH[sum_fw_ingress]="       需放行: tcp/%s (面板) + 你建的入站协议用到的端口。"
I18N_ZHTW[sum_fw_ingress]="       需放行: tcp/%s (面板) + 你建的入站協議用到的連接埠。"
I18N_FA[sum_fw_ingress]="       ورودی موردنیاز: tcp/%s (پنل) + هر پورتی که inboundهای شما استفاده می‌کنند."
I18N_RU[sum_fw_ingress]="       Требуемый вход: tcp/%s (панель) + любые порты, используемые вашими входящими."
I18N_EN[sum_fw_seeweb]="       Open them in your provider's web console (Oracle / AWS / GCP / etc. each have their own security-group page)."
I18N_ZH[sum_fw_seeweb]="       请到你的云服务商控制台 (Oracle / AWS / GCP 等都有自己的安全组页面) 打开端口。"
I18N_ZHTW[sum_fw_seeweb]="       請到你的雲端服務商主控台 (Oracle / AWS / GCP 等都有自己的安全群組頁面) 打開連接埠。"
I18N_FA[sum_fw_seeweb]="       آن‌ها را در کنسول وب ارائه‌دهنده خود باز کنید (Oracle / AWS / GCP / غیره هرکدام صفحه گروه امنیتی مخصوص خود را دارند)."
I18N_RU[sum_fw_seeweb]="       Откройте их в веб-консоли вашего провайдера (Oracle / AWS / GCP / и т.д. — у каждого своя страница групп безопасности)."

I18N_EN[sum_cmds]="🛠 Common commands"
I18N_ZH[sum_cmds]="🛠 常用命令"
I18N_ZHTW[sum_cmds]="🛠 常用命令"
I18N_FA[sum_cmds]="🛠 دستورات رایج"
I18N_RU[sum_cmds]="🛠 Частые команды"
I18N_EN[sum_cmd_menu]="    edgenest                               # management menu (URL / restart / logs / reset password / uninstall)"
I18N_ZH[sum_cmd_menu]="    edgenest                               # 管理菜单 (地址 / 重启 / 日志 / 改密码 / 卸载)"
I18N_ZHTW[sum_cmd_menu]="    edgenest                               # 管理選單 (位址 / 重啟 / 日誌 / 改密碼 / 解除安裝)"
I18N_FA[sum_cmd_menu]="    edgenest                               # منوی مدیریت (آدرس / راه‌اندازی مجدد / لاگ‌ها / بازنشانی رمز / حذف)"
I18N_RU[sum_cmd_menu]="    edgenest                               # меню управления (URL / перезапуск / логи / сброс пароля / удаление)"
I18N_EN[sum_cmd_info]="    edgenest status                        # show panel URL + account anytime"
I18N_ZH[sum_cmd_info]="    edgenest status                        # 随时查看面板地址 + 账号"
I18N_ZHTW[sum_cmd_info]="    edgenest status                        # 隨時查看面板位址 + 帳號"
I18N_FA[sum_cmd_info]="    edgenest status                        # نمایش آدرس پنل + حساب در هر زمان"
I18N_RU[sum_cmd_info]="    edgenest status                        # показать URL панели + аккаунт в любой момент"
I18N_EN[sum_cmd_status]="    sudo systemctl status edgenest         # service status"
I18N_ZH[sum_cmd_status]="    sudo systemctl status edgenest         # 服务状态"
I18N_ZHTW[sum_cmd_status]="    sudo systemctl status edgenest         # 服務狀態"
I18N_FA[sum_cmd_status]="    sudo systemctl status edgenest         # وضعیت سرویس"
I18N_RU[sum_cmd_status]="    sudo systemctl status edgenest         # состояние службы"
I18N_EN[sum_cmd_logs]="    sudo journalctl -u edgenest -f         # live logs"
I18N_ZH[sum_cmd_logs]="    sudo journalctl -u edgenest -f         # 实时日志"
I18N_ZHTW[sum_cmd_logs]="    sudo journalctl -u edgenest -f         # 即時日誌"
I18N_FA[sum_cmd_logs]="    sudo journalctl -u edgenest -f         # لاگ‌های زنده"
I18N_RU[sum_cmd_logs]="    sudo journalctl -u edgenest -f         # логи в реальном времени"
I18N_EN[sum_cmd_restart]="    sudo systemctl restart edgenest        # restart (no output on success)"
I18N_ZH[sum_cmd_restart]="    sudo systemctl restart edgenest        # 重启 (成功时无任何输出, 这是正常的)"
I18N_ZHTW[sum_cmd_restart]="    sudo systemctl restart edgenest        # 重啟 (成功時無任何輸出, 這是正常的)"
I18N_FA[sum_cmd_restart]="    sudo systemctl restart edgenest        # راه‌اندازی مجدد (در صورت موفقیت بدون خروجی)"
I18N_RU[sum_cmd_restart]="    sudo systemctl restart edgenest        # перезапуск (при успехе нет вывода)"
I18N_EN[sum_cmd_reinstall]="    sudo bash scripts/install.sh           # re-run installer (change config / add xray)"
I18N_ZH[sum_cmd_reinstall]="    sudo bash scripts/install.sh           # 重跑安装 (改配置 / 加装 xray)"
I18N_ZHTW[sum_cmd_reinstall]="    sudo bash scripts/install.sh           # 重跑安裝 (改設定 / 加裝 xray)"
I18N_FA[sum_cmd_reinstall]="    sudo bash scripts/install.sh           # اجرای مجدد نصب‌کننده (تغییر پیکربندی / افزودن xray)"
I18N_RU[sum_cmd_reinstall]="    sudo bash scripts/install.sh           # перезапуск установщика (изменить конфиг / добавить xray)"
I18N_EN[sum_cmd_uninstall]="    sudo bash scripts/uninstall.sh         # uninstall (keeps data; add --purge to wipe)"
I18N_ZH[sum_cmd_uninstall]="    sudo bash scripts/uninstall.sh         # 卸载 (默认保留数据; 加 --purge 彻底清)"
I18N_ZHTW[sum_cmd_uninstall]="    sudo bash scripts/uninstall.sh         # 解除安裝 (預設保留資料; 加 --purge 徹底清除)"
I18N_FA[sum_cmd_uninstall]="    sudo bash scripts/uninstall.sh         # حذف (داده‌ها حفظ می‌شود؛ برای پاک‌سازی --purge را اضافه کنید)"
I18N_RU[sum_cmd_uninstall]="    sudo bash scripts/uninstall.sh         # удаление (данные сохраняются; добавьте --purge для полной очистки)"

I18N_EN[binsrc_local]="local"
I18N_ZH[binsrc_local]="本地预编译"
I18N_ZHTW[binsrc_local]="本地預編譯"
I18N_FA[binsrc_local]="محلی"
I18N_RU[binsrc_local]="локальный"
I18N_EN[binsrc_prebuilt]="prebuilt"
I18N_ZH[binsrc_prebuilt]="预编译产物"
I18N_ZHTW[binsrc_prebuilt]="預編譯產物"
I18N_FA[binsrc_prebuilt]="از پیش ساخته‌شده"
I18N_RU[binsrc_prebuilt]="готовая сборка"
I18N_EN[binsrc_source]="source"
I18N_ZH[binsrc_source]="源码构建"
I18N_ZHTW[binsrc_source]="原始碼建置"
I18N_FA[binsrc_source]="منبع"
I18N_RU[binsrc_source]="исходники"

# ---- Vietnamese (vi) translations ----
I18N_VI[need_root]="Vui lòng chạy với quyền root (quản lý systemd/iptables/sysctl/cổng thấp)."
I18N_VI[need_root_hint]="  sudo bash scripts/install.sh"
I18N_VI[unsupported_os]="Hệ điều hành không được hỗ trợ: %s (được hỗ trợ: Debian/Ubuntu/CentOS/Alma/Rocky/Fedora)"
I18N_VI[unsupported_arch]="Kiến trúc không được hỗ trợ: %s"
I18N_VI[lang_prompt]="Language / 语言"
I18N_VI[srv_header]="  ===== Máy chủ ====="
I18N_VI[srv_os]="    OS       : %s"
I18N_VI[srv_kernel]="    Nhân      : %s"
I18N_VI[srv_arch]="    Kiến trúc : %s"
I18N_VI[srv_cpu]="    CPU      : %s vCPU"
I18N_VI[srv_cpu_full]="    CPU      : %s vCPU  (%s nhân vật lý × %s luồng/nhân)"
I18N_VI[srv_cpu_model]="    Kiểu CPU : %s"
I18N_VI[srv_mem]="    Bộ nhớ   : %s"
I18N_VI[srv_ip]="    IP công cộng: %s"
I18N_VI[srv_ip_unknown]="không xác định"
I18N_VI[setup_header]="===== Cài đặt EdgeNest ====="
I18N_VI[setup_intro]="Vui lòng trả lời các câu hỏi bên dưới. Cấu hình nâng cao được thực hiện trong bảng điều khiển web sau khi cài đặt."
I18N_VI[host_hint]="host = địa chỉ công cộng của máy chủ. Dùng tên miền nếu bạn đã cấu hình DNS, nếu không thì dùng IP máy chủ. Có thể chỉnh sửa sau trong bảng điều khiển web."
I18N_VI[host_label]="host"
I18N_VI[port_hint]="Cổng bảng điều khiển web (TCP)"
I18N_VI[port_label]="cổng bảng điều khiển"
I18N_VI[port_preserve]="Phát hiện bản cài đặt hiện có — giữ nguyên cổng bảng điều khiển hiện tại tcp/%s (có thể ghi đè bên dưới)."
I18N_VI[xray_hint1]="Bản cài đặt mặc định kèm các giao thức sing-box (VLESS-Reality / Hysteria2 / Trojan / SS-2022 / TUIC / VMess-WS / VLESS-WS)."
I18N_VI[xray_hint2]="Chọn 'y' nếu bạn cũng muốn các giao thức xray-core (VLESS-XHTTP-Reality / XHTTP-ENC / AnyTLS)."
I18N_VI[xray_label]="cài đặt xray-core v%s"
I18N_VI[confirm_header]="===== Chuẩn bị cài đặt ====="
I18N_VI[confirm_host]="  host         : %s"
I18N_VI[confirm_port]="  cổng bảng điều khiển : %s (TCP)"
I18N_VI[confirm_lang]="  Ngôn ngữ bảng điều khiển : %s   (ngôn ngữ bảng điều khiển giống nhau)"
I18N_VI[confirm_xray_yes]="  xray-core    : có (v%s)"
I18N_VI[confirm_xray_no]="  xray-core    : không"
I18N_VI[confirm_bbr_skip]="  Tinh chỉnh BBR : bỏ qua (--no-bbr)"
I18N_VI[confirm_bbr_on]="  Tinh chỉnh BBR : bật (im lặng)"
I18N_VI[confirm_components]="  Các thành phần sẽ được cài đặt:"
I18N_VI[confirm_pkgs]="    - Gói hệ thống: curl, git, tar, unzip, ca-certificates, sqlite3, iptables, python3"
I18N_VI[confirm_singbox]="    - sing-box v%s"
I18N_VI[confirm_xray_comp]="    - xray-core v%s"
I18N_VI[confirm_edgenest]="    - edgenest v%s"
I18N_VI[confirm_systemd]="    - đơn vị systemd (tự khởi động khi bật máy)"
I18N_VI[confirm_iptables]="    - quy tắc iptables cho cổng bảng điều khiển + iptables-persistent"
I18N_VI[confirm_bbr_line]="    - BBR + fq sysctl (tinh chỉnh TCP nhân)"
I18N_VI[confirm_ntp_line]="    - NTP (đồng bộ đồng hồ hệ thống, cần cho SS-2022 / Hysteria2 / TUIC)"
I18N_VI[ntp_syncing]="Đang đồng bộ đồng hồ hệ thống (SS-2022 / Hy2 từ chối các máy khách lệch > 30s)…"
I18N_VI[ntp_synced]="Đồng hồ đã đồng bộ: lệch %ss so với RTC"
I18N_VI[ntp_skewed]="CẢNH BÁO: lệch đồng hồ so với RTC = %ss. SS-2022 / Hy2 có thể từ chối một số máy khách."
I18N_VI[confirm_go]="xác nhận và bắt đầu"
I18N_VI[cancelled]="Đã hủy."
I18N_VI[ask_default]="[mặc định: %s]"
I18N_VI[deps_installing]="Đang cài đặt các gói hệ thống (curl / unzip / sqlite3 / iptables / python3)…"
I18N_VI[bin_local_found]="Đã tìm thấy ./bin/edgenest cục bộ, đang dùng nó (bỏ qua tải bản dựng sẵn + bỏ qua biên dịch từ mã nguồn)."
I18N_VI[bin_prebuilt_fallback]="Bản phát hành dựng sẵn không khả dụng; quay lại biên dịch từ mã nguồn."
I18N_VI[bin_no_prebuilt]="--no-prebuilt: bỏ qua tải bản phát hành dựng sẵn."
I18N_VI[prebuilt_trying]="Đang thử bản dựng sẵn GitHub Release: %s / linux-%s…"
I18N_VI[prebuilt_ok]="Tệp nhị phân dựng sẵn đã cài đặt: ./bin/edgenest (%s)"
I18N_VI[go_installing]="Đang cài đặt Go %s…"
I18N_VI[go_installed]="Go đã cài đặt: %s"
I18N_VI[node_installing]="Đang cài đặt Node %s.x…"
I18N_VI[node_installed]="Node đã cài đặt: %s + npm %s"
I18N_VI[build_web]="Đang xây dựng SPA front-end (npm ci + build)…"
I18N_VI[build_sync]="Đang đồng bộ SPA vào thư mục nhúng (internal/control/web/dist)…"
I18N_VI[build_go]="Đang xây dựng tệp nhị phân edgenest (go build, nhúng SPA)…"
I18N_VI[build_done]="Xây dựng hoàn tất: ./bin/edgenest (%s)"
I18N_VI[sb_system_ok]="sing-box hệ thống khớp phiên bản cố định + v2ray_api, dùng lại: %s"
I18N_VI[sb_local_found]="Đã tìm thấy sing-box tùy chỉnh cục bộ (with_v2ray_api), đang dùng nó (bỏ qua tải về + biên dịch)."
I18N_VI[sb_release_trying]="Đang thử sing-box từ EdgeNest Release (with_v2ray_api): v%s / linux-%s…"
I18N_VI[sb_release_fallback]="sing-box bản Release không khả dụng; biên dịch từ mã nguồn (scripts/build-singbox.sh)."
I18N_VI[sb_building]="Đang biên dịch sing-box v%s với with_v2ray_api (hạn ngạch theo người dùng cần nó; lần biên dịch đầu tiên mất vài phút)…"
I18N_VI[sb_built]="sing-box đã biên dịch từ mã nguồn: %s"
I18N_VI[sb_installed]="sing-box đã cài đặt: %s"
I18N_VI[sb_fatal]="LỖI NGHIÊM TRỌNG: không thể lấy được sing-box với with_v2ray_api (cần cho hạn ngạch lưu lượng theo người dùng). Hủy bỏ thay vì cài đặt một engine bị thiếu chức năng."
I18N_VI[xray_skip]="Bỏ qua xray-core."
I18N_VI[xray_present]="xray đã có sẵn ở phiên bản cố định, bỏ qua."
I18N_VI[xray_version_mismatch]="xray hệ thống v%s != phiên bản cố định v%s; cài lại phiên bản cố định."
I18N_VI[xray_unsupported]="xray không hỗ trợ kiến trúc: %s"
I18N_VI[xray_downloading]="Đang tải xray-core v%s (%s)…"
I18N_VI[xray_installed]="xray đã cài đặt: %s"
I18N_VI[xray_verify_fail]="LỖI NGHIÊM TRỌNG: mong đợi xray v%s nhưng tệp nhị phân đã cài báo cáo v%s. Hủy bỏ."
I18N_VI[edgenest_installing]="Đang cài đặt tệp nhị phân EdgeNest…"
I18N_VI[edgenest_missing]="./bin/edgenest không tồn tại — xây dựng thất bại?"
I18N_VI[edgenest_installed]="Đã cài đặt: %s"
I18N_VI[setup_dirs]="Đang tạo thư mục dữ liệu (cổng bảng điều khiển = %s)…"
I18N_VI[cap_v4only]="Đã tinh chỉnh thiết lập mạng cho nút chỉ-IPv4 này."
I18N_VI[cap_v6only]="Đã cấu hình bộ giải DNS64 cho nút chỉ-IPv6 này."
I18N_VI[fw_opening]="Đang mở cổng bảng điều khiển trong iptables…"
I18N_VI[fw_skip]="tcp/%s đã mở, bỏ qua"
I18N_VI[fw_opened]="tcp/%s đã mở"
I18N_VI[fw_old_cleaned]="cổng đã thay đổi %s→%s — đã xóa quy tắc iptables cũ cho tcp/%s trước đó"
I18N_VI[fw_done]="iptables đã cấu hình + lưu lại (chỉ bảng điều khiển — cổng giao thức được mở theo nhu cầu từ bảng điều khiển web)"
I18N_VI[bbr_skip]="--no-bbr: bỏ qua tinh chỉnh BBR sysctl."
I18N_VI[bbr_enabling]="Đang bật BBR + fq (kiểm soát tắc nghẽn TCP + qdisc)…"
I18N_VI[bbr_already]="Đã ở BBR, giữ nguyên sysctl."
I18N_VI[bbr_unsupported]="Nhân không có mô-đun tcp_bbr. Giữ kiểm soát tắc nghẽn = %s."
I18N_VI[bbr_enabled]="BBR đã bật (lưu lại qua %s)"
I18N_VI[bbr_failed]="sysctl báo cáo kiểm soát tắc nghẽn = %s (mong đợi bbr)."
I18N_VI[svc_starting]="Đang khởi động dịch vụ edgenest…"
I18N_VI[svc_timeout]="edgenest không khởi động trong 30s, nhật ký:"
I18N_VI[svc_timeout_cred]="Nếu dịch vụ khởi động ngay sau đó, thông tin đăng nhập lần chạy đầu sẽ ở %s/first-run.cred (sudo cat để đọc một lần; hoặc chạy: sudo edgenest reset-pass)."
I18N_VI[svc_path_unrecoverable]="Không thể khôi phục đường dẫn bảng điều khiển từ cơ sở dữ liệu — chạy 'sudo edgenest status' để xem URL bảng điều khiển."
I18N_VI[svc_db_existed]="Phát hiện cơ sở dữ liệu đã lưu — dùng lại tài khoản / inbound / chứng chỉ trước đó của bạn (thông tin đăng nhập ở phần tóm tắt bên dưới)."
I18N_VI[svc_pwd_preserved]="(không đổi — giữ mật khẩu quản trị cũ)"
I18N_VI[sum_title]="            ✅  EdgeNest đã cài đặt thành công"
I18N_VI[sum_svc]="📦 Trạng thái dịch vụ"
I18N_VI[sum_svc_edgenest]="    edgenest   : v%s  (%s, đã bật tự khởi động)"
I18N_VI[svc_state_active]="đang chạy"
I18N_VI[sum_svc_singbox]="    sing-box   : v%s    (engine, %s)"
I18N_VI[sb_src_system]="dùng lại tệp nhị phân hệ thống"
I18N_VI[sb_src_local]="bản dựng sẵn cục bộ"
I18N_VI[sb_src_release]="tải từ release"
I18N_VI[sb_src_source]="biên dịch từ mã nguồn"
I18N_VI[sum_svc_xray_yes]="    xray-core  : v%s    (engine, giao thức tùy chọn)"
I18N_VI[sum_svc_xray_no]="    xray-core  : chưa cài đặt   (chạy lại trình cài đặt để thêm)"
I18N_VI[sum_svc_binsrc]="    nguồn nhị phân : %s"
I18N_VI[sum_bbr_enabled]="    BBR        : ✅ đã bật (sysctl %s → bbr, đã lưu lại)"
I18N_VI[sum_bbr_already]="    BBR        : ✅ đã bật sẵn (nhân %s)"
I18N_VI[sum_bbr_skipped]="    BBR        : đã bỏ qua (--no-bbr)"
I18N_VI[sum_bbr_unsupported]="    BBR        : ⚠️ nhân không có mô-đun tcp_bbr (hiện tại = %s)"
I18N_VI[sum_bbr_failed]="    BBR        : ⚠️ sysctl đã đặt nhưng nhân báo cáo %s"
I18N_VI[sum_login]="🌐 Đăng nhập bảng điều khiển"
I18N_VI[sum_url]="    URL        : %s"
I18N_VI[sum_url_alt]="    Thay thế (v6) : %s"
I18N_VI[sum_url_dual_note]="    Cả hai địa chỉ đều đến cùng một bảng điều khiển. Chứng chỉ tự ký phủ cả hai trong SAN."
I18N_VI[sum_user]="    Tên đăng nhập : %s"
I18N_VI[sum_pwd]="    Mật khẩu   : %s"
I18N_VI[sum_pwd_warn]="    ⚠️ Lần đăng nhập đầu buộc đổi mật khẩu. Hãy lưu mật khẩu ở trên NGAY BÂY GIỜ (sẽ không hiển thị lại)."
I18N_VI[host_detected_dual]="    Đã phát hiện: IPv4 %s   +   IPv6 %s   (hai-ngăn-xếp — cả hai đều đến được bảng điều khiển)"
I18N_VI[host_detected_v4only]="    Đã phát hiện: IPv4 %s   (một-ngăn-xếp IPv4)"
I18N_VI[host_detected_v6only]="    Đã phát hiện: IPv6 %s   (một-ngăn-xếp IPv6)"
I18N_VI[sum_capability]="🌍 Khả năng nút đã phát hiện"
I18N_VI[sum_capability_v4]="    IPv4       : %s"
I18N_VI[sum_capability_v6]="    IPv6       : %s"
I18N_VI[sum_capability_none]="(không thể truy cập)"
I18N_VI[sum_capability_dual_note]="    → Hai-ngăn-xếp: URL đăng ký mặc định dùng v4; trong trình hướng dẫn bạn có thể chuyển sang v6 hoặc chạy trình hướng dẫn hai lần để có hai đăng ký song song trên cùng máy chủ."
I18N_VI[sum_capability_v4only_note]="    → Nút chỉ-IPv4: URL đăng ký dùng địa chỉ này."
I18N_VI[sum_capability_v6only_note]="    → Nút chỉ-IPv6: URL đăng ký dùng địa chỉ này; Kasper DNS64 được cấu hình trong /etc/resolv.conf cho các đích chỉ-v4."
I18N_VI[sum_capability_neither_note]="    → Không họ nào truy cập được — kiểm tra mạng VPS của bạn trước khi tạo inbound."
I18N_VI[sum_next]="👉 Bước tiếp theo"
I18N_VI[sum_next_body]="    Đăng nhập → \"Trình hướng dẫn tạo inbound\" trong thanh bên. Thẻ đầu tiên chọn\n    host đăng ký (IPv4 / IPv6); nút một-ngăn-xếp tự động bị khóa,\n    nút hai-ngăn-xếp cho bạn chọn. Sau đó chọn chế độ:\n      • Nhanh    — một cú nhấp cho 1-4 giao thức phổ biến\n      • Kịch bản — chọn theo trường hợp sử dụng (du lịch / gia đình / ẩn giấu tối đa …)\n      • Đầy đủ   — xác thực tên miền + CDN / Argo"
I18N_VI[sum_next_dual_tip]="    Mẹo: nút hai-ngăn-xếp có thể chạy trình hướng dẫn hai lần — một lần cho IPv4, một lần cho IPv6 — để có hai đăng ký song song trên cùng máy chủ."
I18N_VI[sum_fw]="🔥 Tường lửa"
I18N_VI[sum_fw_local]="    ✅ iptables cục bộ: cổng bảng điều khiển (tcp/%s) đã mở + lưu lại"
I18N_VI[sum_fw_hint]="       Cổng giao thức tự động mở khi bạn tạo inbound trong bảng điều khiển web."
I18N_VI[sum_fw_cloud]="    ⚠️ Nhóm bảo mật đám mây (Oracle / AWS / GCP / Azure / Vultr / DigitalOcean) phải được mở riêng!"
I18N_VI[sum_fw_ingress]="       Lưu lượng vào cần thiết: tcp/%s (bảng điều khiển) + bất kỳ cổng nào inbound của bạn dùng."
I18N_VI[sum_fw_seeweb]="       Mở chúng trong bảng điều khiển web của nhà cung cấp (Oracle / AWS / GCP / v.v. mỗi nơi có trang nhóm bảo mật riêng)."
I18N_VI[sum_cmds]="🛠 Lệnh thường dùng"
I18N_VI[sum_cmd_menu]="    edgenest                               # menu quản lý (URL / khởi động lại / nhật ký / đặt lại mật khẩu / gỡ cài đặt)"
I18N_VI[sum_cmd_info]="    edgenest status                        # hiển thị URL bảng điều khiển + tài khoản bất kỳ lúc nào"
I18N_VI[sum_cmd_status]="    sudo systemctl status edgenest         # trạng thái dịch vụ"
I18N_VI[sum_cmd_logs]="    sudo journalctl -u edgenest -f         # nhật ký trực tiếp"
I18N_VI[sum_cmd_restart]="    sudo systemctl restart edgenest        # khởi động lại (không có đầu ra khi thành công)"
I18N_VI[sum_cmd_reinstall]="    sudo bash scripts/install.sh           # chạy lại trình cài đặt (đổi cấu hình / thêm xray)"
I18N_VI[sum_cmd_uninstall]="    sudo bash scripts/uninstall.sh         # gỡ cài đặt (giữ dữ liệu; thêm --purge để xóa sạch)"
I18N_VI[binsrc_local]="cục bộ"
I18N_VI[binsrc_prebuilt]="dựng sẵn"
I18N_VI[binsrc_source]="mã nguồn"

t() {
  local key="$1"; shift
  local raw
  case "$LANG_CHOICE" in
    zh)    raw="${I18N_ZH[$key]:-${I18N_EN[$key]:-$key}}" ;;
    zh-TW) raw="${I18N_ZHTW[$key]:-${I18N_EN[$key]:-$key}}" ;;
    fa)    raw="${I18N_FA[$key]:-${I18N_EN[$key]:-$key}}" ;;
    ru)    raw="${I18N_RU[$key]:-${I18N_EN[$key]:-$key}}" ;;
    vi)    raw="${I18N_VI[$key]:-${I18N_EN[$key]:-$key}}" ;;
    *)     raw="${I18N_EN[$key]:-$key}" ;;
  esac
  # shellcheck disable=SC2059
  printf "$raw" "$@"
}
tln() { t "$@"; printf "\n"; }

# ---------------------------------------------------------------------------
# Language detection + picker
# ---------------------------------------------------------------------------

detect_default_lang() {
  if [ -n "${EDGENEST_LANG:-}" ]; then echo "$EDGENEST_LANG"; return; fi
  case "${LANG:-}${LC_ALL:-}${LC_MESSAGES:-}" in
    *zh_TW*|*zh_HK*|*zh-TW*) echo "zh-TW" ;;
    *zh*|*ZH*) echo "zh" ;;
    *fa*|*fa_IR*) echo "fa" ;;
    *ru*|*RU*) echo "ru" ;;
    *vi*|*vi_VN*) echo "vi" ;;
    *) echo "en" ;;
  esac
}

ask_language() {
  if [ -n "$LANG_CHOICE" ]; then return; fi   # --lang= already set it
  local default; default=$(detect_default_lang)
  if [ "$ASSUME_YES" = "1" ] || [ ! -e /dev/tty ]; then
    LANG_CHOICE="$default"
    return
  fi
  printf "\n${C_CYAN}? Language / 语言${C_RESET} ${C_DIM}[1] English  [2] 中文  [3] 繁體中文  [4] فارسی  [5] Русский  [6] Tiếng Việt  (default: %s)${C_RESET}: " "$default"
  local input
  read -r input </dev/tty || input=""
  case "$input" in
    1|en|EN|english|English) LANG_CHOICE="en" ;;
    2|zh|ZH|cn|CN|chinese|Chinese|中文) LANG_CHOICE="zh" ;;
    3|zh-TW|zh-tw|zh_TW|zhtw|ZHTW|tw|TW|繁體中文|繁体中文|繁中) LANG_CHOICE="zh-TW" ;;
    4|fa|FA|farsi|Farsi|persian|Persian|فارسی) LANG_CHOICE="fa" ;;
    5|ru|RU|russian|Russian|Русский|русский) LANG_CHOICE="ru" ;;
    6|vi|VI|vn|VN|vietnamese|Vietnamese|"tiếng việt"|"Tiếng Việt"|"tieng viet") LANG_CHOICE="vi" ;;
    *) LANG_CHOICE="$default" ;;
  esac
  case "$LANG_CHOICE" in
    zh)    green "${I18N_ZH[lang_set_zh]}" ;;
    zh-TW) green "${I18N_ZHTW[lang_set_zhtw]}" ;;
    fa)    green "${I18N_FA[lang_set_fa]}" ;;
    ru)    green "${I18N_RU[lang_set_ru]}" ;;
    vi)    green "${I18N_VI[lang_set_vi]}" ;;
    *)     green "${I18N_EN[lang_set_en]}" ;;
  esac
}

# ---------------------------------------------------------------------------
# OS / arch detection
# ---------------------------------------------------------------------------

require_root() {
  if [ "$(id -u)" -ne 0 ]; then
    red "$(t need_root)"
    red "$(t need_root_hint)"
    exit 1
  fi
}

detect_os() {
  if [ -f /etc/os-release ]; then . /etc/os-release; OS_ID="${ID:-unknown}"; OS_NAME="${PRETTY_NAME:-$OS_ID}"; else OS_ID="unknown"; OS_NAME="unknown"; fi
  case "$OS_ID" in
    debian|ubuntu|centos|almalinux|rocky|fedora) : ;;
    *) red "$(t unsupported_os "$OS_ID")"; exit 1 ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)  ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) red "$(t unsupported_arch "$(uname -m)")"; exit 1 ;;
  esac
}

# ---------------------------------------------------------------------------
# Banner + server info
# ---------------------------------------------------------------------------

print_banner() {
  # Banner: Copperplate "EDGENEST" rasterized to Braille (chafa) and floated
  # in the middle of a Braille-blank star-field that fills the FULL terminal
  # width. Stars are single-dot Braille glyphs (⠁ ⠂ ⠄ ⠈ ⠐ ⠠ ⡀ ⢀) so they
  # share the exact cell width of the letter glyphs — no ASCII/Braille
  # alignment drift — and read as "deep space" rather than ascii dots.
  local cols left right
  cols=$(tput cols 2>/dev/null || echo 100)
  local LOGO_W=80
  left=$(( (cols - LOGO_W) / 2 ))
  [ $left -lt 1 ] && left=1
  right=$(( cols - LOGO_W - left ))
  [ $right -lt 1 ] && right=1

  # Two star tiers so the sky reads as depth, not noise:
  #   - dim tier (Braille single-dot, 8 dot positions): the diffuse "dust"
  #   - bright tier (ASCII *): the rare standout star
  # Both ASCII * and Braille glyphs are 1 cell wide in monospace fonts so
  # column alignment is preserved across mixed rows.
  local -a STAR=("⠁" "⠂" "⠄" "⠈" "⠐" "⠠" "⡀" "⢀")
  local BLANK="⠀"  # U+2800 Braille blank — same width as letter glyphs

  # sky_row $width $seed — emits a row of $width cells with dim stars at
  # ~1 per 17 and bright * at ~1 per 53 (≈ 3× rarer). Cheap LCG keeps the
  # render deterministic per (seed,i) so identical runs look identical.
  sky_row() {
    local w=$1 seed=$2 i out=""
    for ((i=0; i<w; i++)); do
      local n=$(( (seed * 1103515245 + i * 12345 + 1013904223) & 0x7fffffff ))
      if (( n % 53 == 0 )); then
        out+="*"
      elif (( n % 17 == 0 )); then
        out+="${STAR[n % 8]}"
      else
        out+="$BLANK"
      fi
    done
    printf "%s" "$out"
  }

  # text_row $text $seed — center $text on cols, pad both sides with sky.
  # Used for the footer lines so version/author land inside the star field
  # rather than floating in a void below it.
  text_row() {
    local text=$1 seed=$2
    local tlen=${#text}
    local ltext=$(( (cols - tlen) / 2 ))
    [ $ltext -lt 1 ] && ltext=1
    local rtext=$(( cols - tlen - ltext ))
    [ $rtext -lt 1 ] && rtext=1
    printf "%s%s%s" "$(sky_row "$ltext" "$seed")" "$text" "$(sky_row "$rtext" $((seed+1)))"
  }

  echo ""
  # 3 pure star-field rows above the wordmark.
  printf "${C_DIM}"
  for s in 1 2 3; do printf "%s\n" "$(sky_row "$cols" $((s*31)))"; done
  # Logo rows: continuous star-field on each side, letters in the middle.
  printf "${C_RESET}${C_GREEN}${C_BOLD}"
  local row=0
  while IFS= read -r line; do
    row=$((row+1))
    local L="$(sky_row "$left" $((row*53+100)))"
    local R="$(sky_row "$right" $((row*53+200)))"
    printf "%s%s%s\n" "$L" "$line" "$R"
  done <<'LOGO'
⢶⣶⣶⣶⣶⣶⣶⣶⡇⠰⣶⣶⣶⣶⣶⣶⣦⣄⠀⠀⠀⣀⣤⣶⣶⣶⣶⣦⣤⠄⠀⢲⣶⣶⣶⣶⣶⣶⣶⡇⠀⢶⣶⣶⡀⠀⠀⠀⢶⣶⡆⠀⠰⣶⣶⣶⣶⣶⣶⣶⣿⠀⢀⣤⣶⣶⣶⣶⣦⣤⡄⢸⣶⣶⣶⣶⣶⣶⣶⣾
⢸⣿⡏⠉⠉⠉⡉⠉⠃⠀⣿⣿⠉⠉⠉⠉⠻⣿⣷⠀⣼⣿⡟⠋⠉⠉⠉⠻⠏⠀⠀⢸⣿⡏⠉⠉⠉⡉⠉⠃⠀⢸⣿⣿⣿⣦⡀⠀⢸⣿⡇⠀⠀⣿⣿⠉⠉⠉⢉⠉⠙⠀⣾⣿⣏⠉⠉⠉⠛⡟⠀⠘⠉⠉⢹⣿⣟⠉⠉⠙
⢸⣿⣿⣿⣿⣿⡇⠀⠀⠀⣿⣿⠀⠀⠀⠀⠀⣿⣿⠄⣿⣿⠀⠀⠀⣦⣤⣤⣤⡤⠀⢸⣿⣿⣿⣿⣿⡇⠀⠀⠀⢸⣿⡏⠻⣿⣷⣄⢸⣿⡇⠀⠀⣿⣿⣿⣿⣿⣿⠀⠀⠀⠹⢿⣿⣿⣿⣿⣿⣶⡀⠀⠀⠀⢸⣿⣇⠀⠀⠀
⢸⣿⡇⠀⠀⠀⠁⢀⡀⠀⣿⣿⠀⠀⠀⢀⣠⣿⡿⠀⢻⣿⣆⡀⠀⠋⠉⣹⣿⡇⠀⢸⣿⡇⠀⠀⠀⠁⠀⡀⠀⢸⣿⡇⠀⠈⠻⣿⣿⣿⡇⠀⠀⣿⣿⠀⠀⠀⠈⠀⣀⠀⢠⣆⣀⠉⠉⢉⣹⣿⡇⠀⠀⠀⢸⣿⣇⠀⠀⠀
⣼⣿⣿⣿⣿⣿⣿⣿⡇⢠⣿⣿⣿⣿⣿⣿⠿⠟⠁⠀⠀⠙⠿⠿⣿⣿⣿⠿⠟⠃⠀⣼⣿⣿⣿⣿⣿⣿⣿⡇⠀⣼⣿⡇⠀⠀⠀⠙⣿⣿⡇⠀⢠⣿⣿⣿⣿⣿⣿⣿⣿⠀⠛⠿⢿⣿⣿⡿⠿⠛⠁⠀⠀⠀⢸⣿⣿⠀⠀⠀
LOGO
  # 2 pure star-field rows below the wordmark, then footer-in-sky, then
  # 1 more sky row to close the frame.
  printf "${C_RESET}${C_DIM}"
  printf "%s\n" "$(sky_row "$cols" 124)"
  printf "%s\n" "$(sky_row "$cols" 155)"
  # Footer lines embedded in the star-field — text in the middle, sky on
  # both sides, so the eye never crosses an empty void between the logo
  # and the version/author info.
  local foot1="v${EDGENEST_VERSION}  ·  by AiPo <aipo@ailenshow.com>  ·  AGPL-3.0"
  local foot2="https://github.com/aipo-lenshow/EdgeNest"
  printf "%s\n" "$(text_row "$foot1" 186)"
  printf "%s\n" "$(text_row "$foot2" 217)"
  printf "%s\n" "$(sky_row "$cols" 248)"
  printf "${C_RESET}\n"
}

detect_cpu_info() {
  CPU_VCPU=$(nproc 2>/dev/null || echo "?")
  CPU_CORES=""; CPU_THREADS=""; CPU_MODEL=""
  if command -v lscpu >/dev/null 2>&1; then
    local sockets cps tpc model
    sockets=$(lscpu | awk -F: '/^Socket\(s\):/ {gsub(/[[:space:]]/,"",$2); print $2; exit}')
    cps=$(lscpu     | awk -F: '/^Core\(s\) per socket:/ {gsub(/[[:space:]]/,"",$2); print $2; exit}')
    tpc=$(lscpu     | awk -F: '/^Thread\(s\) per core:/ {gsub(/[[:space:]]/,"",$2); print $2; exit}')
    model=$(lscpu   | awk -F: '/^Model name:/ {sub(/^[[:space:]]+/,"",$2); print $2; exit}')
    if [ -n "$sockets" ] && [ -n "$cps" ] && [ -n "$tpc" ]; then
      CPU_CORES=$((sockets * cps))
      CPU_THREADS="$tpc"
    fi
    [ -n "$model" ] && CPU_MODEL="$model"
  fi
}

print_server_info() {
  local kernel mem_total ip ip_show
  kernel=$(uname -r)
  if [ -r /proc/meminfo ]; then
    local kb
    kb=$(awk '/MemTotal/ {print $2}' /proc/meminfo)
    mem_total=$(awk -v k="$kb" 'BEGIN {printf "%.1f GB", k/1024/1024}')
  else
    mem_total="?"
  fi
  detect_cpu_info
  ip=$(detect_public_ip)
  if [ -n "$ip" ]; then ip_show="$ip"; else ip_show="$(t srv_ip_unknown)"; fi

  printf "${C_BOLD}%s${C_RESET}\n" "$(t srv_header)"
  tln srv_os     "$OS_NAME"
  tln srv_kernel "$kernel"
  tln srv_arch   "$ARCH"
  if [ -n "$CPU_CORES" ]; then
    tln srv_cpu_full "$CPU_VCPU" "$CPU_CORES" "$CPU_THREADS"
  else
    tln srv_cpu "$CPU_VCPU"
  fi
  if [ -n "$CPU_MODEL" ]; then tln srv_cpu_model "$CPU_MODEL"; fi
  tln srv_mem    "$mem_total"
  tln srv_ip     "$ip_show"
  echo ""
}

# ---------------------------------------------------------------------------
# Interactive helpers
# ---------------------------------------------------------------------------

ask() {
  local var="$1" prompt_key="$2" default="$3" input
  local prompt; prompt="$(t "$prompt_key")"
  printf "${C_CYAN}? ${prompt}${C_RESET} ${C_DIM}%s${C_RESET}: " "$(t ask_default "$default")"
  if [ "$ASSUME_YES" = "1" ]; then
    printf "%s ${C_DIM}(--yes)${C_RESET}\n" "$default"
    input="$default"
  elif [ -e /dev/tty ]; then
    read -r input </dev/tty || input=""
  else
    read -r input || input=""
  fi
  [ -z "$input" ] && input="$default"
  eval "$var=\"\$input\""
}

ask_yes_no() {
  local var="$1" prompt_key="$2" default="$3" input hint_str
  local prompt; prompt="$(t "$prompt_key" "$XRAY_VERSION")"
  if [ "$default" = "y" ]; then hint_str="[Y/n]"; else hint_str="[y/N]"; fi
  printf "${C_CYAN}? ${prompt}${C_RESET} ${C_DIM}%s${C_RESET}: " "$hint_str"
  if [ "$ASSUME_YES" = "1" ]; then
    printf "%s ${C_DIM}(--yes)${C_RESET}\n" "$default"
    input="$default"
  elif [ -e /dev/tty ]; then
    read -r input </dev/tty || input=""
  else
    read -r input || input=""
  fi
  input=${input:-$default}
  case "$input" in
    y|Y|yes|YES) eval "$var=1" ;;
    *)           eval "$var=0" ;;
  esac
}

detect_public_ip() {
  local ip=""
  ip=$(curl -fsSL -m 5 https://api.ipify.org 2>/dev/null) \
    || ip=$(curl -fsSL -m 5 https://ifconfig.me 2>/dev/null) \
    || ip=$(curl -fsSL -m 5 https://ipinfo.io/ip 2>/dev/null) \
    || ip=""
  echo "$ip"
}

# detect_public_ip_v4 / detect_public_ip_v6 force a specific family so we can
# tell the operator "your node has both" up front, in ask_user_config (which
# runs before detect_node_capability writes network.json) and again in the
# summary. icanhazip is the same probe detect_node_capability uses, so both
# numbers agree.
detect_public_ip_v4() {
  # Must never propagate curl's non-zero exit: the script runs under
  # `set -euo pipefail`, so a failing pipeline aborts the installer
  # before ask_user_config can show anything. On a v4-only host the v6
  # probe must fail silently and return an empty string; same in reverse.
  local out=""
  out=$(curl -fsS4 -m 5 https://icanhazip.com -k 2>/dev/null) || out=""
  printf '%s' "$out" | tr -d '[:space:]'
}
detect_public_ip_v6() {
  local out=""
  out=$(curl -fsS6 -m 5 https://icanhazip.com -k 2>/dev/null) || out=""
  printf '%s' "$out" | tr -d '[:space:]'
}

# On re-run / upgrade, read the panel port from the existing systemd unit's
# --listen so a non-interactive (--yes) upgrade preserves the operator's chosen
# port instead of silently resetting to DEFAULT_PANEL_PORT. Handles both
# 0.0.0.0:PORT and [::]:PORT forms (strip up to the last colon). BUGLOG 0-1.
detect_existing_panel_port() {
  [ -f "$SYSTEMD_UNIT" ] || return 1
  local listen port
  listen=$(grep -oE -- '--listen [^ ]+' "$SYSTEMD_UNIT" 2>/dev/null | head -1 | awk '{print $2}')
  [ -n "$listen" ] || return 1
  port="${listen##*:}"
  case "$port" in
    ''|*[!0-9]*) return 1 ;;
    *) printf '%s' "$port"; return 0 ;;
  esac
}

ask_user_config() {
  printf "${C_BOLD}%s${C_RESET}\n" "$(t setup_header)"
  hint "$(t setup_intro)"
  echo ""

  # Probe both families up front so the operator can see what they have
  # before they pick a panel host. PROBED_V4 / PROBED_V6 are also reused
  # by print_summary to show the alternate URL on dual-stack hosts.
  PROBED_V4=$(detect_public_ip_v4)
  PROBED_V6=$(detect_public_ip_v6)
  local detected_ip
  if [ -n "$PROBED_V4" ] && [ -n "$PROBED_V6" ]; then
    tln host_detected_dual "$PROBED_V4" "$PROBED_V6"
    detected_ip="$PROBED_V4"
  elif [ -n "$PROBED_V4" ]; then
    tln host_detected_v4only "$PROBED_V4"
    detected_ip="$PROBED_V4"
  elif [ -n "$PROBED_V6" ]; then
    tln host_detected_v6only "$PROBED_V6"
    detected_ip="$PROBED_V6"
  else
    detected_ip=$(detect_public_ip)
  fi
  hint "$(t host_hint)"
  ask HOST host_label "${detected_ip:-127.0.0.1}"
  echo ""

  hint "$(t port_hint)"
  local port_default="$DEFAULT_PANEL_PORT"
  OLD_PANEL_PORT="$(detect_existing_panel_port || true)"
  if [ -n "$OLD_PANEL_PORT" ]; then
    port_default="$OLD_PANEL_PORT"
    hint "$(t port_preserve "$OLD_PANEL_PORT")"
  fi
  ask PANEL_PORT port_label "$port_default"
  echo ""

  hint "$(t xray_hint1)"
  hint "$(t xray_hint2)"
  ask_yes_no INSTALL_XRAY xray_label "y"
  echo ""

  printf "${C_BOLD}%s${C_RESET}\n" "$(t confirm_header)"
  tln confirm_host "$HOST"
  tln confirm_port "$PANEL_PORT"
  case "$LANG_CHOICE" in
    zh)    lang_display="中文" ;;
    zh-TW) lang_display="繁體中文" ;;
    fa)    lang_display="فارسی" ;;
    ru)    lang_display="Русский" ;;
    vi)    lang_display="Tiếng Việt" ;;
    en)    lang_display="English" ;;
    *)     lang_display="$LANG_CHOICE" ;;
  esac
  tln confirm_lang "$lang_display"
  if [ "$INSTALL_XRAY" = "1" ]; then tln confirm_xray_yes "$XRAY_VERSION"; else tln confirm_xray_no; fi
  if [ "$NO_BBR" = "1" ]; then tln confirm_bbr_skip; else tln confirm_bbr_on; fi
  echo ""
  tln confirm_components
  tln confirm_pkgs
  tln confirm_singbox "$SINGBOX_VERSION"
  if [ "$INSTALL_XRAY" = "1" ]; then tln confirm_xray_comp "$XRAY_VERSION"; fi
  tln confirm_edgenest "$EDGENEST_VERSION"
  tln confirm_systemd
  tln confirm_iptables
  if [ "$NO_BBR" = "0" ]; then tln confirm_bbr_line; fi
  echo ""
  ask_yes_no CONFIRM_GO confirm_go "y"
  if [ "$CONFIRM_GO" != "1" ]; then red "$(t cancelled)"; exit 0; fi
  echo ""
}

# ---------------------------------------------------------------------------
# System deps
# ---------------------------------------------------------------------------

install_deps() {
  info "$(t deps_installing)"
  if command -v apt-get >/dev/null 2>&1; then
    apt-get update -qq
    apt-get install -y -qq curl git tar unzip ca-certificates sqlite3 iptables python3 >/dev/null
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y -q curl git tar unzip ca-certificates sqlite iptables python3 >/dev/null
  elif command -v yum >/dev/null 2>&1; then
    yum install -y -q curl git tar unzip ca-certificates sqlite iptables python3 >/dev/null
  fi
}

# ---------------------------------------------------------------------------
# edgenest binary: try prebuilt release first, fall back to source build
# ---------------------------------------------------------------------------

_binary_arch_matches() {
  # Returns 0 if $1 is an ELF binary matching the target $ARCH.
  # Guards against accidentally shipping a dev-host binary (e.g. macOS arm64 ./bin/edgenest)
  # in the source tarball, which install.sh would otherwise blindly install → 203/EXEC.
  #
  # Strategy: read the ELF header directly. `file` would work too but Ubuntu 24.04
  # minimal images and many cloud-init "headless" templates don't ship it, and the
  # grep-on-`file`-output check used to silently return 1 on those hosts — sending
  # install.sh down the source-build path even when we had a perfectly good
  # cross-compiled binary in the tarball. ELF spec is stable so the header read
  # is the more robust gate; `file` lookup stays as a belt-and-suspenders pass
  # when available.
  local f="$1"
  [ -x "$f" ] || return 1
  local h; h=$(head -c 4 "$f" 2>/dev/null | od -An -tx1 | tr -d ' \n')
  [ "$h" = "7f454c46" ] || return 1  # ELF magic
  # ELF header byte 18-19 is e_machine (little-endian). amd64 = 0x3E, arm64 = 0xB7.
  local em; em=$(dd if="$f" bs=1 skip=18 count=2 2>/dev/null | od -An -tx1 | tr -d ' \n')
  case "$ARCH" in
    amd64)
      [ "$em" = "3e00" ] || return 1
      if command -v file >/dev/null 2>&1; then
        file "$f" 2>/dev/null | grep -q 'x86-64' || return 1
      fi
      ;;
    arm64)
      [ "$em" = "b700" ] || return 1
      if command -v file >/dev/null 2>&1; then
        file "$f" 2>/dev/null | grep -qE 'aarch64|ARM aarch64' || return 1
      fi
      ;;
    *) return 1 ;;
  esac
}

ensure_edgenest_binary() {
  local candidate=""
  if _binary_arch_matches "./bin/edgenest-linux-${ARCH}"; then
    candidate="./bin/edgenest-linux-${ARCH}"
  elif _binary_arch_matches "./bin/edgenest"; then
    candidate="./bin/edgenest"
  fi
  if [ -n "$candidate" ]; then
    if [ "$candidate" != "./bin/edgenest" ]; then
      install -m 0755 "$candidate" ./bin/edgenest
    fi
    info "$(t bin_local_found)"
    INSTALL_SOURCE="local"
    return
  fi
  if [ -e "./bin/edgenest" ] || [ -e "./bin/edgenest-linux-${ARCH}" ]; then
    yellow "Local ./bin/edgenest* present but architecture mismatch (need linux/${ARCH}); ignoring."
    rm -f ./bin/edgenest
  fi

  if [ "$NO_PREBUILT" = "0" ]; then
    if try_prebuilt_release; then
      INSTALL_SOURCE="prebuilt"
      return
    fi
    yellow "$(t bin_prebuilt_fallback)"
  else
    info "$(t bin_no_prebuilt)"
  fi

  ensure_build_toolchain
  build_edgenest
  INSTALL_SOURCE="source"
}

try_prebuilt_release() {
  local tag="v${EDGENEST_VERSION}"
  local url="${EDGENEST_RELEASE_BASE}/${tag}/edgenest-${EDGENEST_VERSION}-linux-${ARCH}.tar.gz"
  info "$(t prebuilt_trying "$tag" "$ARCH")"
  local tmp; tmp="$(mktemp -d)"
  if ! curl -fsSL -m 30 "$url" -o "$tmp/edgenest.tar.gz" 2>/dev/null; then
    rm -rf "$tmp"
    return 1
  fi
  if ! tar -xzf "$tmp/edgenest.tar.gz" -C "$tmp" 2>/dev/null; then
    rm -rf "$tmp"
    return 1
  fi
  mkdir -p bin
  if [ -x "$tmp/edgenest" ]; then
    install -m 0755 "$tmp/edgenest" ./bin/edgenest
  elif [ -x "$tmp/bin/edgenest" ]; then
    install -m 0755 "$tmp/bin/edgenest" ./bin/edgenest
  else
    # tar may contain a versioned subdir (edgenest-<ver>-linux-<arch>/edgenest)
    local found
    found=$(find "$tmp" -type f -name edgenest -perm -u+x | head -1)
    if [ -n "$found" ]; then
      install -m 0755 "$found" ./bin/edgenest
    else
      rm -rf "$tmp"
      return 1
    fi
  fi
  rm -rf "$tmp"
  green "$(t prebuilt_ok "$(du -h ./bin/edgenest | awk '{print $1}')")"
  return 0
}

ensure_build_toolchain() {
  local need_go=0 need_node=0
  if ! command -v go >/dev/null 2>&1; then
    need_go=1
  else
    local gv
    gv=$(go version | awk '{print $3}' | sed 's/go//')
    if ! printf '%s\n%s\n' "1.22" "$gv" | sort -V -C; then
      need_go=1
    fi
  fi
  if ! command -v npm >/dev/null 2>&1; then need_node=1; fi

  if [ $need_go = 1 ]; then install_go; fi
  if [ $need_node = 1 ]; then install_node; fi
}

install_go() {
  info "$(t go_installing "$GO_VERSION")"
  local url="https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz"
  curl -fsSL "$url" -o /tmp/go.tar.gz
  rm -rf /usr/local/go
  tar -C /usr/local -xzf /tmp/go.tar.gz
  rm -f /tmp/go.tar.gz
  export PATH=/usr/local/go/bin:$PATH
  if ! grep -q '/usr/local/go/bin' /etc/profile 2>/dev/null; then
    echo 'export PATH=$PATH:/usr/local/go/bin' >> /etc/profile
  fi
  green "$(t go_installed "$(go version)")"
}

install_node() {
  info "$(t node_installing "$NODE_MAJOR")"
  if command -v apt-get >/dev/null 2>&1; then
    curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash - >/dev/null
    apt-get install -y -qq nodejs >/dev/null
  elif command -v dnf >/dev/null 2>&1; then
    curl -fsSL "https://rpm.nodesource.com/setup_${NODE_MAJOR}.x" | bash - >/dev/null
    dnf install -y -q nodejs >/dev/null
  elif command -v yum >/dev/null 2>&1; then
    curl -fsSL "https://rpm.nodesource.com/setup_${NODE_MAJOR}.x" | bash - >/dev/null
    yum install -y -q nodejs >/dev/null
  fi
  green "$(t node_installed "$(node --version)" "$(npm --version)")"
}

build_edgenest() {
  info "$(t build_web)"
  (cd web && npm ci --silent --prefer-offline --no-audit --no-fund && npm run build --silent)
  info "$(t build_sync)"
  rm -rf internal/control/web/dist
  mkdir -p internal/control/web/dist
  cp -r web/dist/. internal/control/web/dist/
  info "$(t build_go)"
  mkdir -p bin
  CGO_ENABLED=0 GOMEMLIMIT=512MiB \
    go build -trimpath -ldflags="-s -w" -o ./bin/edgenest ./cmd/edgenest
  green "$(t build_done "$(du -h ./bin/edgenest | awk '{print $1}')")"
}

# ---------------------------------------------------------------------------
# Engine binaries
# ---------------------------------------------------------------------------

# Verify a sing-box binary is BOTH the pinned version AND carries the
# with_v2ray_api build tag. EdgeNest's per-user traffic quota / accounting reads
# sing-box's experimental.v2ray_api StatsService, which the OFFICIAL release
# binary omits (it's gated behind that build tag — see scripts/build-singbox.sh).
# A binary missing the tag installs and runs fine but silently breaks quota
# enforcement for every sing-box user, so we never accept one that fails this.
# `sing-box version` prints a "Tags: …,with_v2ray_api" line we can grep.
_singbox_verify() {
  local bin="$1" out
  [ -x "$bin" ] || return 1
  out="$("$bin" version 2>/dev/null)" || return 1
  printf '%s\n' "$out" | grep -qE "version ${SINGBOX_VERSION}([^0-9]|$)" || return 1
  printf '%s\n' "$out" | grep -q "with_v2ray_api" || return 1
}

# Lightweight Go-only toolchain guard for the source-build fallback (the full
# ensure_build_toolchain also pulls Node, which sing-box doesn't need).
ensure_go_toolchain() {
  if command -v go >/dev/null 2>&1; then
    local gv; gv=$(go version | awk '{print $3}' | sed 's/go//')
    # sing-box 1.13.13 go.mod requires go 1.24.7 — reuse the system Go only if it
    # meets that floor, else install_go (GO_VERSION=1.25.0 ≥ 1.24.7). Re-check this
    # floor against the new go.mod whenever SINGBOX_VERSION is bumped.
    if printf '%s\n%s\n' "1.24.7" "$gv" | sort -V -C; then return; fi
  fi
  install_go
}

# Try EdgeNest's own GitHub Release asset for the custom (with_v2ray_api) build.
# Asset name: sing-box-<sbver>-with_v2ray_api-linux-<arch>.tar.gz under the
# edgenest release tag. Verified before acceptance; any miss returns 1 so the
# caller falls through to the source build.
try_release_singbox() {
  local tag="v${EDGENEST_VERSION}"
  local url="${EDGENEST_RELEASE_BASE}/${tag}/sing-box-${SINGBOX_VERSION}-with_v2ray_api-linux-${ARCH}.tar.gz"
  info "$(t sb_release_trying "$SINGBOX_VERSION" "$ARCH")"
  local tmp; tmp="$(mktemp -d)"
  if ! curl -fsSL -m 60 "$url" -o "$tmp/sb.tar.gz" 2>/dev/null; then rm -rf "$tmp"; return 1; fi
  if ! tar -xzf "$tmp/sb.tar.gz" -C "$tmp" 2>/dev/null; then rm -rf "$tmp"; return 1; fi
  local found
  found=$(find "$tmp" -type f -name 'sing-box*' ! -name '*.tar.gz' | head -1)
  if [ -z "$found" ]; then rm -rf "$tmp"; return 1; fi
  chmod 0755 "$found"
  if ! _singbox_verify "$found"; then rm -rf "$tmp"; return 1; fi
  install -m 0755 "$found" "$INSTALL_BIN/sing-box"
  rm -rf "$tmp"
  return 0
}

# Obtain a sing-box WITH with_v2ray_api via a four-tier fallback, mirroring
# ensure_edgenest_binary. Every tier is verified; we NEVER silently fall back to
# the official release binary (that would cripple quotas). git clone is
# self-sufficient: scripts/build-singbox.sh ships in the repo, so the source
# build is always reachable even with no tarball and no release asset.
ensure_singbox_binary() {
  # 0. A system sing-box already on PATH — reuse ONLY if it's our pinned version
  #    AND has the tag. A stray official / ygkkk-script sing-box lacks the tag.
  local sys; sys="$(command -v sing-box 2>/dev/null || true)"
  if [ -n "$sys" ] && _singbox_verify "$sys"; then
    [ "$sys" = "$INSTALL_BIN/sing-box" ] || install -m 0755 "$sys" "$INSTALL_BIN/sing-box"
    SINGBOX_SOURCE="system"
    green "$(t sb_system_ok "$($INSTALL_BIN/sing-box version | head -1)")"
    return
  fi

  # 1. Local prebuilt in the source tree (deploy tarball / operator-placed /
  #    a prior build-singbox.sh run). Versioned name first, then bare bin/sing-box.
  local cand=""
  for c in "./bin/sing-box-${SINGBOX_VERSION}-linux-${ARCH}" "./bin/sing-box"; do
    if _binary_arch_matches "$c" && _singbox_verify "$c"; then cand="$c"; break; fi
  done
  if [ -n "$cand" ]; then
    install -m 0755 "$cand" "$INSTALL_BIN/sing-box"
    SINGBOX_SOURCE="local"
    info "$(t sb_local_found)"
    green "$(t sb_installed "$($INSTALL_BIN/sing-box version | head -1)")"
    return
  fi

  # 2. EdgeNest GitHub Release asset (fast path for clone users once published).
  if [ "$NO_PREBUILT" = "0" ] && try_release_singbox; then
    SINGBOX_SOURCE="release"
    green "$(t sb_installed "$($INSTALL_BIN/sing-box version | head -1)")"
    return
  fi

  # 3. Build from source — the git-clone-self-sufficient guarantee.
  yellow "$(t sb_release_fallback)"
  if [ ! -f scripts/build-singbox.sh ]; then red "$(t sb_fatal)"; exit 1; fi
  ensure_go_toolchain
  info "$(t sb_building "$SINGBOX_VERSION")"
  SINGBOX_VERSION="$SINGBOX_VERSION" GOOS=linux GOARCH="$ARCH" bash scripts/build-singbox.sh
  local built="./bin/sing-box-${SINGBOX_VERSION}-linux-${ARCH}"
  if ! _singbox_verify "$built"; then red "$(t sb_fatal)"; exit 1; fi
  install -m 0755 "$built" "$INSTALL_BIN/sing-box"
  SINGBOX_SOURCE="source"
  green "$(t sb_built "$($INSTALL_BIN/sing-box version | head -1)")"
}

install_xray() {
  [ "$INSTALL_XRAY" != "1" ] && { info "$(t xray_skip)"; return; }
  # xray-core's stats (app/stats + app/commander) ship in the official binary,
  # so no custom build is needed — but we still pin the version: reuse a system
  # xray only if it already matches XRAY_VERSION, otherwise (re)install the pin.
  if command -v xray >/dev/null 2>&1; then
    local cur; cur=$(xray version 2>/dev/null | head -1 | awk '{print $2}')
    if [ "$cur" = "$XRAY_VERSION" ]; then
      info "$(t xray_present)"
      return
    fi
    yellow "$(t xray_version_mismatch "${cur:-?}" "$XRAY_VERSION")"
  fi
  local zip_arch
  case "$ARCH" in
    amd64) zip_arch="64" ;;
    arm64) zip_arch="arm64-v8a" ;;
    *) red "$(t xray_unsupported "$ARCH")"; return ;;
  esac
  info "$(t xray_downloading "$XRAY_VERSION" "$zip_arch")"
  local url="https://github.com/XTLS/Xray-core/releases/download/v${XRAY_VERSION}/Xray-linux-${zip_arch}.zip"
  local tmp; tmp="$(mktemp -d)"
  curl -fsSL "$url" -o "$tmp/xray.zip"
  unzip -o "$tmp/xray.zip" xray geoip.dat geosite.dat -d "$tmp/extract/" >/dev/null
  install -m 0755 "$tmp/extract/xray" "$INSTALL_BIN/xray"
  mkdir -p "$XRAY_SHARE_DIR"
  install -m 0644 "$tmp/extract/geoip.dat"   "$XRAY_SHARE_DIR/geoip.dat"
  install -m 0644 "$tmp/extract/geosite.dat" "$XRAY_SHARE_DIR/geosite.dat"
  rm -rf "$tmp"
  local got; got=$($INSTALL_BIN/xray version 2>/dev/null | head -1 | awk '{print $2}')
  if [ "$got" != "$XRAY_VERSION" ]; then
    red "$(t xray_verify_fail "$XRAY_VERSION" "${got:-?}")"
    exit 1
  fi
  green "$(t xray_installed "$($INSTALL_BIN/xray version | head -1)")"
}

install_edgenest() {
  info "$(t edgenest_installing)"
  if [ ! -f "./bin/edgenest" ]; then
    red "$(t edgenest_missing)"
    exit 1
  fi
  install -m 0755 ./bin/edgenest "$INSTALL_BIN/edgenest"
  green "$(t edgenest_installed "$INSTALL_BIN/edgenest")"
}

setup_dirs_and_service() {
  # Silent step (no banner): creating dirs / writing the unit / enabling the
  # service is plumbing the operator doesn't need narrated. Output picks back up
  # at the firewall step.
  mkdir -p "$DATA_DIR" "$DATA_DIR/certs" "$LOG_DIR"
  chmod 750 "$DATA_DIR"

  # Self-host the hostname so `sudo` doesn't print
  # "unable to resolve host <name>: Name or service not known" on every
  # invocation. Some terminal emulators (FinalShell) hold the prompt cursor
  # after a stderr warning long enough that the operator thinks `sudo
  # systemctl restart edgenest` is hanging — the command actually finishes
  # in <200ms. One line in /etc/hosts kills the warning permanently.
  local _hn
  _hn=$(hostname 2>/dev/null)
  if [ -n "$_hn" ] && ! grep -qE "[[:space:]]${_hn}([[:space:]]|$)" /etc/hosts 2>/dev/null; then
    # Trailing marker lets uninstall.sh remove exactly this line and nothing else.
    printf "127.0.1.1 %s # added by edgenest installer\n" "$_hn" >> /etc/hosts
  fi

  # Seed the panel's first-load language to match what the operator picked
  # during install. Backend reads this file once on first start, persists it
  # into the default_lang setting, then it's effectively no-op forever after.
  printf "%s\n" "$LANG_CHOICE" > "$DATA_DIR/install.lang"
  chmod 644 "$DATA_DIR/install.lang"

  # Stash a copy of the uninstaller in the data dir so the `edgenest` menu's
  # Uninstall option (and a future re-run) can find it even after the extracted
  # source tree is deleted. install.sh's CWD is the repo root (see top of file).
  if [ -f scripts/uninstall.sh ]; then
    cp scripts/uninstall.sh "$DATA_DIR/uninstall.sh"
    chmod 755 "$DATA_DIR/uninstall.sh"
  fi

  # Choose the panel listen address based on the family the kernel can
  # actually bind. detect_node_capability later in install.sh disables v6
  # via sysctl on v4-only nodes, so binding to "[::]:port" there would
  # fail at startup and leave the operator locked out. We can't read
  # network.json yet (detect hasn't run — it depends on the dirs this
  # function creates), so probe the kernel directly: if any global v6 is
  # configured right now, default to dual-stack wildcard; otherwise to
  # the v4 wildcard. Mirrors the same rule the inbound's listenForHost
  # helper applies. The Go binary also has a runtime listen fallback if
  # this guess turns out wrong (e.g. capability changed after install).
  local panel_listen="0.0.0.0:${PANEL_PORT}"
  if ip -6 addr show scope global 2>/dev/null | grep -q 'inet6 '; then
    panel_listen="[::]:${PANEL_PORT}"
  fi

  # Restart=always (NOT on-failure): the in-panel restore path intentionally
  # exits 0 to self-relaunch and fold in the staged DB at boot. With on-failure
  # systemd treats exit 0 as success and never restarts, leaving the panel down
  # with the restore unapplied. RestartSec gates against tight loops; an explicit
  # 'systemctl stop' is still honored. Do not revert to on-failure.
  #
  # NOTE: this heredoc is UNQUOTED (<<EOF) so ${INSTALL_BIN}/${panel_listen}/
  # ${DATA_DIR} expand — keep its body free of backticks and $(...) or they run
  # as command substitutions (a stray `systemctl stop` here once printed "Too
  # few arguments." mid-install and corrupted the written line). Comments above,
  # not inside.
  cat > "$SYSTEMD_UNIT" <<EOF
[Unit]
Description=EdgeNest proxy node panel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=${INSTALL_BIN}/edgenest --role standalone --listen ${panel_listen} --data-dir ${DATA_DIR}
Restart=always
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
EOF

  systemctl daemon-reload
  systemctl enable edgenest >/dev/null 2>&1
}

# ---------------------------------------------------------------------------
# Firewall (panel port only; protocol ports are opened by the web panel
# when the operator creates inbounds)
# ---------------------------------------------------------------------------

configure_firewall() {
  info "$(t fw_opening)"
  # Tag the panel rule with the same `edgenest-managed` comment the daemon uses,
  # so uninstall.sh's tagged-rule flush removes it too. Untagged, the panel port
  # stayed ACCEPTed forever after uninstall (and got re-persisted into rules.v4).
  if iptables -C INPUT -p tcp --dport "$PANEL_PORT" -m comment --comment edgenest-managed -j ACCEPT 2>/dev/null; then
    hint "$(t fw_skip "$PANEL_PORT")"
  else
    iptables -I INPUT -p tcp --dport "$PANEL_PORT" -m comment --comment edgenest-managed -j ACCEPT
    hint "$(t fw_opened "$PANEL_PORT")"
  fi

  # Port changed on this upgrade: drop the stale ACCEPT rule(s) for the old
  # panel port so we don't leave an orphaned open port behind. BUGLOG 0-2.
  # Clean both the tagged form (current) and the legacy untagged form (rules
  # created before the tag was added) so an upgrade reconciles either shape.
  if [ -n "$OLD_PANEL_PORT" ] && [ "$OLD_PANEL_PORT" != "$PANEL_PORT" ]; then
    while iptables -C INPUT -p tcp --dport "$OLD_PANEL_PORT" -m comment --comment edgenest-managed -j ACCEPT 2>/dev/null; do
      iptables -D INPUT -p tcp --dport "$OLD_PANEL_PORT" -m comment --comment edgenest-managed -j ACCEPT 2>/dev/null || break
    done
    while iptables -C INPUT -p tcp --dport "$OLD_PANEL_PORT" -j ACCEPT 2>/dev/null; do
      iptables -D INPUT -p tcp --dport "$OLD_PANEL_PORT" -j ACCEPT 2>/dev/null || break
    done
    hint "$(t fw_old_cleaned "$OLD_PANEL_PORT" "$PANEL_PORT" "$OLD_PANEL_PORT")"
  fi

  if command -v netfilter-persistent >/dev/null 2>&1; then
    netfilter-persistent save >/dev/null 2>&1 || true
  elif command -v apt-get >/dev/null 2>&1; then
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq iptables-persistent >/dev/null 2>&1 || true
    netfilter-persistent save >/dev/null 2>&1 || true
  elif [ -d /etc/sysconfig ] && command -v iptables-save >/dev/null 2>&1; then
    iptables-save > /etc/sysconfig/iptables 2>/dev/null || true
  fi
  green "$(t fw_done)"
}

# ---------------------------------------------------------------------------
# BBR + fq (silent — no prompt)
# ---------------------------------------------------------------------------

ensure_time_sync() {
  # SS-2022 / Hysteria2 / TUIC all carry a client timestamp the server checks
  # against its own clock with a ±30 s replay-protection window. Many VPS
  # images ship with systemd-timesyncd active but never actually reaching an
  # NTP server (provider firewall blocks UDP 123, or the configured pool is
  # unreachable from the region), and the clock drifts. The symptom is
  # SS-2022 silently rejecting every connection with `bad timestamp diff Ns`,
  # which the operator can't debug from the panel. Force-sync at install:
  #  1) hwclock --hctosys: copy the (battery-backed) RTC to the system clock
  #     so we're at most a few seconds off even if NTP never reaches anyone.
  #  2) Point systemd-timesyncd at Cloudflare/Google so it has a real chance
  #     to stay synced after the install (Cloudflare runs on port 123 over
  #     UDP/TCP; if the VPS blocks UDP, NTS-KE/TCP at least tries).
  #  3) Verify final drift; surface a WARNING if > 5 s.
  info "$(t ntp_syncing)"
  if command -v hwclock >/dev/null 2>&1; then
    hwclock --hctosys 2>/dev/null || true
  fi
  if command -v timedatectl >/dev/null 2>&1; then
    mkdir -p /etc/systemd
    cat > /etc/systemd/timesyncd.conf <<'EOF'
# Written by EdgeNest installer. Multiple servers so a single firewall block
# doesn't strand the clock. Cloudflare + Google + ntp.org pool.
[Time]
NTP=time.cloudflare.com time1.google.com time2.google.com
FallbackNTP=pool.ntp.org 0.pool.ntp.org 1.pool.ntp.org
EOF
    timedatectl set-ntp true >/dev/null 2>&1 || true
    systemctl restart systemd-timesyncd 2>/dev/null || true
  fi
  local drift_sec
  if command -v hwclock >/dev/null 2>&1; then
    local sys_ts rtc_ts
    sys_ts=$(date -u +%s)
    rtc_ts=$(date -u -d "$(hwclock -r 2>/dev/null | sed 's/\.[0-9]*+/+/')" +%s 2>/dev/null || echo "$sys_ts")
    drift_sec=$((sys_ts > rtc_ts ? sys_ts - rtc_ts : rtc_ts - sys_ts))
  else
    drift_sec=0
  fi
  if [ "$drift_sec" -le 5 ]; then
    green "$(t ntp_synced "$drift_sec")"
  else
    yellow "$(t ntp_skewed "$drift_sec")"
  fi
}

detect_node_capability() {
  # Probe ALL v4 / v6 addresses the host has configured and can dial out
  # through. Write the full list to /etc/edgenest/network.json. EdgeNest's
  # wizard reads these lists via /api/v1/system/capability so the user can
  # pick which IP each protocol inbound binds to.
  #
  # Two-stage probe:
  #   1. `ip -4/-6 addr show scope global` — enumerate every globally-scoped
  #      address bound to a non-loopback interface. Catches multi-IP VPS that
  #      a single curl-out probe would miss (curl picks one outbound IP, not
  #      all of them).
  #   2. curl-out per IP via `--interface <ip>` (or fallback bind-source) —
  #      verify the IP actually reaches the public internet. Some providers
  #      hand out a /29 with internal-only IPs, or filter outbound on alt-IPs;
  #      the probe is the truth, the routing table is the bookkeeping.
  #
  # Outcomes:
  #   - v4-only  (v4>=1 IP, v6=0): sysctl disable v6 system-wide so OS DNS
  #     resolvers stop returning AAAA. The "v6 stays untouched" directive was
  #     reconsidered: on a v4-only VPS there's no global v6 path
  #     anyway, so disabling v6 has zero downside (apt / SSH would already
  #     be falling back to v4). Without this, SOCKS5 / Hy2 / VLESS clients
  #     resolve dual-stack origins per RFC 6724 v6-first, hand the literal v6
  #     destination to sing-box, and outbound/direct hangs on "network is
  #     unreachable" with no v4 fallback — the original IPv6-only DNS bug.
  #   - dual-stack (v4>=1, v6>=1): leave sysctl alone, both families work.
  #   - v6-only  (v4=0, v6>=1): write Kasper public DNS64 to /etc/resolv.conf
  #     so v4-only origins reach via the 64:ff9b::/96 NAT64 prefix.
  local sysctl_v6_file="/etc/sysctl.d/99-edgenest-v6.conf"
  local v4_list=() v6_list=()
  # Stage 1: enumerate global-scope addresses.
  while IFS= read -r addr; do
    [ -n "$addr" ] && v4_list+=("$addr")
  done < <(ip -4 addr show scope global 2>/dev/null | awk '/inet /{sub(/\/.*/, "", $2); print $2}')
  while IFS= read -r addr; do
    [ -n "$addr" ] && v6_list+=("$addr")
  done < <(ip -6 addr show scope global 2>/dev/null | awk '/inet6 /{sub(/\/.*/, "", $2); print $2}')

  # Stage 2: curl-probe each IP and RECORD THE RESPONSE — what the public
  # internet sees when traffic egresses through this interface — not the
  # configured iface IP. On NAT'd VPS (Oracle Cloud, GCP, AWS with EIP)
  # the iface IP is a private RFC1918 address that no client can dial;
  # icanhazip echoes back the upstream-attached public IP, which is the
  # canonical value for the subscription URI. On direct-attached hosts
  # (DigitalOcean, Vultr small, bare-metal) iface IP == public IP and
  # this is a no-op. Dedup at the end — multiple NIC's might NAT to the
  # same upstream public IP.
  local v4_addrs=() v6_addrs=()
  local ip pub
  for ip in "${v4_list[@]}"; do
    pub=$(curl -fsS4 --interface "$ip" --max-time 5 https://icanhazip.com -k 2>/dev/null | tr -d '[:space:]')
    if [ -n "$pub" ]; then
      local already=0
      for existing in "${v4_addrs[@]}"; do
        if [ "$existing" = "$pub" ]; then already=1; break; fi
      done
      [ "$already" = "0" ] && v4_addrs+=("$pub")
    fi
  done
  for ip in "${v6_list[@]}"; do
    pub=$(curl -fsS6 --interface "$ip" --max-time 5 https://icanhazip.com -k 2>/dev/null | tr -d '[:space:]')
    if [ -n "$pub" ]; then
      local already=0
      for existing in "${v6_addrs[@]}"; do
        if [ "$existing" = "$pub" ]; then already=1; break; fi
      done
      [ "$already" = "0" ] && v6_addrs+=("$pub")
    fi
  done

  # If `ip addr` enumeration came up empty for a family but the system-default
  # outbound for that family works, fall back to the single-IP probe — covers
  # exotic environments where `scope global` reports nothing but a default
  # route actually exists. icanhazip echoes the literal egress IP.
  if [ ${#v4_addrs[@]} -eq 0 ]; then
    local out=""
    out=$(curl -fsS4 --max-time 5 https://icanhazip.com -k 2>/dev/null) || true
    out=$(printf '%s' "$out" | tr -d '[:space:]')
    [ -n "$out" ] && v4_addrs+=("$out")
  fi
  if [ ${#v6_addrs[@]} -eq 0 ]; then
    local out=""
    out=$(curl -fsS6 --max-time 5 https://icanhazip.com -k 2>/dev/null) || true
    out=$(printf '%s' "$out" | tr -d '[:space:]')
    [ -n "$out" ] && v6_addrs+=("$out")
  fi

  local has_v4=false has_v6=false
  [ ${#v4_addrs[@]} -gt 0 ] && has_v4=true
  [ ${#v6_addrs[@]} -gt 0 ] && has_v6=true

  # Render JSON arrays. Keep ipv4_addr / ipv6_addr (singular) as the first
  # entry of each list — back-compat for code paths still reading the legacy
  # field (cert SAN bootstrap, share resolver fallback, /api/v1/system/info).
  local v4_first="" v6_first=""
  [ ${#v4_addrs[@]} -gt 0 ] && v4_first="${v4_addrs[0]}"
  [ ${#v6_addrs[@]} -gt 0 ] && v6_first="${v6_addrs[0]}"
  local v4_json="[]" v6_json="[]"
  if [ ${#v4_addrs[@]} -gt 0 ]; then
    v4_json="[$(printf '"%s",' "${v4_addrs[@]}" | sed 's/,$//')]"
  fi
  if [ ${#v6_addrs[@]} -gt 0 ]; then
    v6_json="[$(printf '"%s",' "${v6_addrs[@]}" | sed 's/,$//')]"
  fi

  mkdir -p /etc/edgenest
  cat > /etc/edgenest/network.json <<EOF
{
  "ipv4": $has_v4,
  "ipv4_addr": "$v4_first",
  "ipv4_addrs": $v4_json,
  "ipv6_global": $has_v6,
  "ipv6_addr": "$v6_first",
  "ipv6_addrs": $v6_json
}
EOF
  logger -t edgenest-install "detect_node_capability: ipv4=$has_v4 (${#v4_addrs[@]} ip), ipv6_global=$has_v6 (${#v6_addrs[@]} ip) — written to /etc/edgenest/network.json" 2>/dev/null || true

  # v4-only node: sysctl disable v6 to prevent the dual-stack DNS hang.
  if [ "$has_v4" = "true" ] && [ "$has_v6" = "false" ]; then
    sysctl -w net.ipv6.conf.all.disable_ipv6=1 >/dev/null 2>&1 || true
    sysctl -w net.ipv6.conf.default.disable_ipv6=1 >/dev/null 2>&1 || true
    cat > "$sysctl_v6_file" <<'EOF'
# Written by EdgeNest installer on a v4-only node. The system has no global
# IPv6 path, so the kernel disable here only blocks the link-local fe80::*
# noise that would otherwise leak into getaddrinfo and confuse SOCKS5 / Hy2 /
# VLESS clients (RFC 6724 v6-first dual-stack DNS → sing-box dials v6 →
# "network is unreachable" → client hangs).
# uninstall.sh --purge restores the kernel defaults.
net.ipv6.conf.all.disable_ipv6 = 1
net.ipv6.conf.default.disable_ipv6 = 1
EOF
    info "$(t cap_v4only)"
  elif [ -f "$sysctl_v6_file" ]; then
    # Capability changed away from v4-only (e.g., operator added v6) — restore
    # kernel defaults so the new family actually works. Internal plumbing: log
    # it (journal), don't narrate paths to the operator.
    rm -f "$sysctl_v6_file"
    sysctl -w net.ipv6.conf.all.disable_ipv6=0 >/dev/null 2>&1 || true
    sysctl -w net.ipv6.conf.default.disable_ipv6=0 >/dev/null 2>&1 || true
    logger -t edgenest-install "capability changed — removed $sysctl_v6_file, v6 sysctl restored" 2>/dev/null || true
  fi

  # v6-only: Kasper DNS64 so v4 origins still resolve.
  local backup="/etc/edgenest/resolv.conf.pre-edgenest"
  if [ "$has_v4" = "false" ] && [ "$has_v6" = "true" ]; then
    if [ ! -f "$backup" ] && [ -f /etc/resolv.conf ]; then
      cp -f /etc/resolv.conf "$backup" 2>/dev/null || true
      logger -t edgenest-install "backed up /etc/resolv.conf → $backup" 2>/dev/null || true
    fi
    cat > /etc/resolv.conf <<'EOF'
# EdgeNest: v6-only host, Kasper public DNS64 (RFC 6147).
# Remove this file (or restore from /etc/edgenest/resolv.conf.pre-edgenest)
# if you want to opt out of DNS64.
nameserver 2a00:1098:2b::1
nameserver 2a00:1098:2c::1
EOF
    info "$(t cap_v6only)"
  else
    if [ -f "$backup" ]; then
      cp -f "$backup" /etc/resolv.conf 2>/dev/null || true
      rm -f "$backup"
      logger -t edgenest-install "restored /etc/resolv.conf from $backup (capability changed away from v6-only)" 2>/dev/null || true
    fi
  fi
}

enable_bbr() {
  if [ "$NO_BBR" = "1" ]; then
    info "$(t bbr_skip)"
    BBR_RESULT="skipped"
    return
  fi
  info "$(t bbr_enabling)"
  BBR_BEFORE=$(sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null || echo "?")
  if [ "$BBR_BEFORE" = "bbr" ]; then
    BBR_RESULT="already"
    BBR_AFTER="bbr"
    hint "$(t bbr_already)"
    return
  fi
  # Load the congestion-control + qdisc modules that ship lazily on some kernels
  # (Oracle Cloud, minimal cloud images): tcp_bbr = the algorithm, sch_fq = the
  # recommended qdisc. A missing fq must NOT block bbr — bbr works with the
  # default qdisc too, so set fq best-effort and decide solely on bbr below.
  modprobe tcp_bbr 2>/dev/null || true
  modprobe sch_fq 2>/dev/null || true
  sysctl -w net.core.default_qdisc=fq >/dev/null 2>&1 || true
  if ! sysctl -w net.ipv4.tcp_congestion_control=bbr >/dev/null 2>&1; then
    BBR_RESULT="unsupported"
    BBR_AFTER="$BBR_BEFORE"
    yellow "$(t bbr_unsupported "$BBR_BEFORE")"
    return
  fi
  # Persist module loads + sysctl so BBR survives reboot. Only pin fq if we
  # actually got it (some kernels lack sch_fq) — bbr alone is what matters.
  local qdisc_now; qdisc_now=$(sysctl -n net.core.default_qdisc 2>/dev/null || echo "")
  {
    echo "tcp_bbr"
    if [ "$qdisc_now" = "fq" ]; then echo "sch_fq"; fi
  } > /etc/modules-load.d/edgenest-bbr.conf 2>/dev/null || true
  {
    echo "# Written by EdgeNest installer. Remove this file to revert to kernel defaults."
    if [ "$qdisc_now" = "fq" ]; then echo "net.core.default_qdisc = fq"; fi
    echo "net.ipv4.tcp_congestion_control = bbr"
  } > "$BBR_SYSCTL_FILE"
  BBR_AFTER=$(sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null || echo "?")
  if [ "$BBR_AFTER" = "bbr" ]; then
    BBR_RESULT="enabled"
    green "$(t bbr_enabled "$BBR_SYSCTL_FILE")"
  else
    BBR_RESULT="failed"
    yellow "$(t bbr_failed "$BBR_AFTER")"
  fi
}

# ---------------------------------------------------------------------------
# Start + capture bootstrap credentials
# ---------------------------------------------------------------------------

start_and_capture() {
  info "$(t svc_starting)"
  systemctl restart edgenest

  local i=0
  while [ $i -lt 30 ]; do
    if curl -fsS "http://127.0.0.1:${PANEL_PORT}/api/health" >/dev/null 2>&1; then
      break
    fi
    sleep 1
    i=$((i+1))
  done
  if [ $i -ge 30 ]; then
    red "$(t svc_timeout)"
    journalctl -u edgenest -n 40 --no-pager
    # If the service comes up after this timeout it still writes its one-shot
    # credentials file; point the operator at it so a slow first boot doesn't
    # silently lose the admin password (else: sudo edgenest reset-pass).
    yellow "$(t svc_timeout_cred "$DATA_DIR")"
    exit 1
  fi

  # On first run the service writes a root-only 0600 one-shot file with the
  # credentials — it deliberately never logs the password or the secret panel
  # path to journald (those would survive uninstall and stay readable forever
  # via `journalctl`). Read it once here, then shred it: from this point the
  # plaintext password is unrecoverable except by `edgenest reset-pass`.
  local cred_file="$DATA_DIR/first-run.cred"
  # Initialise before the existence check: on a re-install over a preserved DB
  # there is no first run, so the service writes no cred file and these would
  # stay unset — tripping `set -u` ("unbound variable") at the -z test below.
  BOOTSTRAP_PANEL_PATH=""; BOOTSTRAP_USERNAME=""; BOOTSTRAP_PASSWORD=""
  if [ -f "$cred_file" ]; then
    BOOTSTRAP_PANEL_PATH=$(grep '^PANEL_PATH=' "$cred_file" | head -1 | cut -d= -f2-)
    BOOTSTRAP_USERNAME=$(grep  '^USERNAME='   "$cred_file" | head -1 | cut -d= -f2-)
    BOOTSTRAP_PASSWORD=$(grep  '^PASSWORD='   "$cred_file" | head -1 | cut -d= -f2-)
    rm -f "$cred_file"
  fi

  if [ -z "$BOOTSTRAP_PANEL_PATH" ] || [ -z "$BOOTSTRAP_PASSWORD" ]; then
    yellow "$(t svc_db_existed)"
    BOOTSTRAP_PANEL_PATH=$(sqlite3 "$DATA_DIR/edgenest.db" "SELECT value FROM settings WHERE key='panel_path';" 2>/dev/null | sed 's/^\///') || true
    BOOTSTRAP_USERNAME=$(sqlite3 "$DATA_DIR/edgenest.db" "SELECT username FROM admins ORDER BY id LIMIT 1;" 2>/dev/null) || true
    BOOTSTRAP_USERNAME=${BOOTSTRAP_USERNAME:-EdgeNest}
    BOOTSTRAP_PASSWORD="$(t svc_pwd_preserved)"
    # Still empty? The DB had no panel_path row (corrupt / pre-migration). Warn
    # instead of letting print_summary render a dead root URL the panel won't serve.
    # NB: keep this an if/fi, not `[ -z X ] && yellow` — as the last statement in
    # this branch the &&-form returns 1 whenever the path IS recovered (the normal
    # reinstall case), which under `set -e` aborts the whole script before
    # print_summary ever runs (the panel then comes up with no summary printed).
    if [ -z "$BOOTSTRAP_PANEL_PATH" ]; then
      yellow "$(t svc_path_unrecoverable)"
    fi
  fi
  # Defensive: never let this function's exit status depend on the last expression
  # evaluated above — main() calls us bare under `set -e`, so a stray non-zero
  # return would skip print_summary.
  return 0
}

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------

binsrc_label() {
  case "$INSTALL_SOURCE" in
    local)    t binsrc_local ;;
    prebuilt) t binsrc_prebuilt ;;
    *)        t binsrc_source ;;
  esac
}

sb_src_label() {
  case "$SINGBOX_SOURCE" in
    system)  t sb_src_system ;;
    local)   t sb_src_local ;;
    release) t sb_src_release ;;
    *)       t sb_src_source ;;
  esac
}

print_summary() {
  local sb_ver xray_ver
  sb_ver=$($INSTALL_BIN/sing-box version 2>/dev/null | head -1 | awk '{print $3}')
  if [ "$INSTALL_XRAY" = "1" ]; then
    xray_ver=$($INSTALL_BIN/xray version 2>/dev/null | head -1 | awk '{print $2}')
  fi

  echo ""
  printf "${C_GREEN}${C_BOLD}═══════════════════════════════════════════════════════════════${C_RESET}\n"
  printf "${C_GREEN}${C_BOLD}%s${C_RESET}\n" "$(t sum_title)"
  printf "${C_GREEN}${C_BOLD}═══════════════════════════════════════════════════════════════${C_RESET}\n"
  echo ""

  printf "${C_BOLD}%s${C_RESET}\n" "$(t sum_svc)"
  # Translate systemd's "active" to the user-facing "运行" / "running"; the
  # raw English token in a localized output was confusing in 14d feedback.
  local svc_state
  svc_state=$(systemctl is-active edgenest 2>/dev/null)
  if [ "$svc_state" = "active" ]; then svc_state=$(t svc_state_active); fi
  tln sum_svc_edgenest "$EDGENEST_VERSION" "$svc_state"
  tln sum_svc_singbox  "$sb_ver" "$(sb_src_label)"
  if [ "$INSTALL_XRAY" = "1" ]; then
    tln sum_svc_xray_yes "${xray_ver:-?}"
  else
    tln sum_svc_xray_no
  fi
  tln sum_svc_binsrc "$(binsrc_label)"
  case "$BBR_RESULT" in
    enabled)     green "$(t sum_bbr_enabled "$BBR_BEFORE")" ;;
    already)     green "$(t sum_bbr_already "$BBR_AFTER")" ;;
    skipped)     tln sum_bbr_skipped ;;
    unsupported) yellow "$(t sum_bbr_unsupported "$BBR_BEFORE")" ;;
    failed)      yellow "$(t sum_bbr_failed "$BBR_AFTER")" ;;
  esac
  echo ""

  # Capability section: state the detected outbound IPs explicitly so the
  # operator never has to grep network.json. Only emit the line for a family
  # that actually has an address — listing "IPv6 : (不可达)" on every
  # single-stack install was noise per 14d feedback.
  printf "${C_BOLD}%s${C_RESET}\n" "$(t sum_capability)"
  if [ -n "$PROBED_V4" ]; then tln sum_capability_v4 "$PROBED_V4"; fi
  if [ -n "$PROBED_V6" ]; then tln sum_capability_v6 "$PROBED_V6"; fi
  if [ -n "$PROBED_V4" ] && [ -n "$PROBED_V6" ]; then
    hint "$(t sum_capability_dual_note)"
  elif [ -n "$PROBED_V4" ]; then
    hint "$(t sum_capability_v4only_note)"
  elif [ -n "$PROBED_V6" ]; then
    hint "$(t sum_capability_v6only_note)"
  else
    red "$(t sum_capability_neither_note)"
  fi
  echo ""

  printf "${C_BOLD}%s${C_RESET}\n" "$(t sum_login)"
  printf "${C_GREEN}$(t sum_url "http://${HOST}:${PANEL_PORT}/${BOOTSTRAP_PANEL_PATH}")${C_RESET}\n"
  # If the operator picked a v4 host but v6 is also reachable, show the v6
  # URL so they know they can dial the panel via the other family too. v6
  # literals must be bracketed in URLs; we only add the alt when the user's
  # chosen HOST is the v4 address (not a custom domain — there the DNS
  # records carry both families already).
  if [ -n "$PROBED_V4" ] && [ -n "$PROBED_V6" ] && [ "$HOST" = "$PROBED_V4" ]; then
    printf "${C_GREEN}$(t sum_url_alt "http://[${PROBED_V6}]:${PANEL_PORT}/${BOOTSTRAP_PANEL_PATH}")${C_RESET}\n"
    hint "$(t sum_url_dual_note)"
  fi
  printf "${C_YELLOW}$(t sum_user "$BOOTSTRAP_USERNAME")${C_RESET}\n"
  printf "${C_YELLOW}$(t sum_pwd "$BOOTSTRAP_PASSWORD")${C_RESET}\n"
  red "$(t sum_pwd_warn)"
  echo ""

  printf "${C_BOLD}%s${C_RESET}\n" "$(t sum_next)"
  printf "%b\n" "$(t sum_next_body)"
  # Dual-stack tip highlighted in cyan (not red — 14d feedback: red is
  # painful on FinalShell). Only emit when both families are reachable;
  # on single-stack hosts the tip is irrelevant.
  if [ -n "$PROBED_V4" ] && [ -n "$PROBED_V6" ]; then
    printf "${C_CYAN}%b${C_RESET}\n" "$(t sum_next_dual_tip)"
  fi
  echo ""

  printf "${C_BOLD}%s${C_RESET}\n" "$(t sum_fw)"
  green "$(t sum_fw_local "$PANEL_PORT")"
  hint  "$(t sum_fw_hint)"
  red   "$(t sum_fw_cloud)"
  tln sum_fw_ingress "$PANEL_PORT"
  tln sum_fw_seeweb
  echo ""

  printf "${C_BOLD}%s${C_RESET}\n" "$(t sum_cmds)"
  green "$(t sum_cmd_menu)"
  tln sum_cmd_info
  tln sum_cmd_status
  tln sum_cmd_logs
  tln sum_cmd_restart
  tln sum_cmd_reinstall
  tln sum_cmd_uninstall
  echo ""

  printf "${C_GREEN}${C_BOLD}═══════════════════════════════════════════════════════════════${C_RESET}\n"
  echo ""
}

main() {
  require_root
  detect_os
  detect_arch
  print_banner
  ask_language
  print_server_info
  ask_user_config
  install_deps
  ensure_edgenest_binary
  ensure_singbox_binary
  install_xray
  install_edgenest
  setup_dirs_and_service
  configure_firewall
  ensure_time_sync
  enable_bbr
  detect_node_capability
  start_and_capture
  print_summary
}

main "$@"
