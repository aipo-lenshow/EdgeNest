# EdgeNest

**[English](README.md) · [简体中文](README_ZH.md) · [繁體中文](README_ZH-TW.md) · [فارسی](README_FA.md) · [Русский](README_RU.md) · [Tiếng Việt](README_VI.md)**

> A self-hosted proxy node management panel — dual-engine, wizard-driven, one-command deploy.

[![License](https://img.shields.io/badge/license-AGPL--3.0-blue.svg)](./LICENSE)
![Version](https://img.shields.io/badge/version-1.20.0626-green.svg)
![Engine](https://img.shields.io/badge/engine-sing--box%20%2B%20Xray-orange.svg)

EdgeNest helps users in network-restricted environments reach AI tools, technical documentation, and learning resources reliably. A single command brings the panel, subscription delivery, and proxy engines up on your own VPS, managing multi-protocol inbounds, traffic quotas, certificates, and outbound optimization in one place — all through a graphical interface, with no hand-editing of config files.

---

## Screenshots

_The panel ships in 6 languages — switch the README language above to see the screenshots localized._

**All 11 inbound protocols at a glance — popularity, whether a domain is needed, CDN / Argo support, and network resilience.**

![Protocol guide](docs/screenshots/en/protocols.jpg)

**Pick the client apps you'll use — EdgeNest tailors each inbound and generates a ready-to-import config per client.**

![Pick clients](docs/screenshots/en/clients.jpg)

**Optional CDN front — clients connect via a Cloudflare preferred IP for speed, while the real server IP stays private.**

![CDN front](docs/screenshots/en/cdn.jpg)

**Optional Argo tunnel — clients connect through a Cloudflare tunnel, with no need to expose the server IP or open a port.**

![Argo tunnel](docs/screenshots/en/argo.jpg)

---

## Features

**Protocols & engines**
- **11 inbound protocols** — VLESS-Reality, VLESS-WS, VMess-WS, Trojan-TLS, Hysteria2, TUIC v5, Shadowsocks-2022, AnyTLS, SOCKS5, plus VLESS-XHTTP-Reality / VLESS-XHTTP-TLS on the Xray engine
- **Two engines as one** — sing-box and Xray hosted side by side, so a single program covers a wider range of protocols
- **Wizard-driven creation** — recommends a protocol mix by use case and by your client; beginner-friendly
- **Deep client tuning** — for 13 mainstream clients (Shadowrocket, v2rayN, V2RayNG, Hiddify, Stash, Surge, sing-box, Karing, Mihomo Party, Loon, Quantumult X, and more), subscriptions are generated in each client's own format and connect on import, with no manual config edits

**Users & delivery**
- **Multi-user with traffic quotas** — independent credentials per user, with traffic quotas, expiry dates, and resets
- **Subscription delivery** — generate subscriptions that connect on import; QR codes and one-tap sharing included

**Access & outbound optimization**
- **Access optimization built in** — CDN preferred-IP, Argo tunnels, and WARP outbound, all configured inside the panel in one tap
- **One-click category routing** — route traffic by category (AI, streaming, developer tools, ad-blocking, and more) to WARP / direct / block
- **Service reachability checks** — check in one tap whether the current node can reach various streaming and AI services
- **Routing from real traffic** — capture the domains you actually visit in real time and generate routing rules for each client in one tap

**Operations & security**
- **Certificate management** — self-signed certificates work out of the box; with a domain you can issue Let's Encrypt certificates via either HTTP or DNS validation
- **IPv4 / IPv6 dual stack** — dual-stack inbounds and outbounds; IPv6-only nodes work fine too
- **Telegram management bot** — query, manage, and receive alerts, all from chat
- **Backup and restore** — database and certificates packaged together, with encrypted backups
- **Privacy and security** — per-user credentials, a firewall that opens only the ports actually used, self-signed Hysteria2 pinned by certificate fingerprint against MITM, and logs that can mask client IPs
- **One-command install and uninstall** — deploy in a single command; uninstall leaves nothing behind

---

## Quick Start

Two ways to install — pick either. Right after install, note the printed credentials and change the password on first login.

**Requirements:** a fresh 64-bit Linux VPS (see [Supported Platforms](#supported-platforms)) with root and a working package manager + internet. The installer auto-installs everything it needs (curl, git, sqlite3, iptables, …) and prefers prebuilt binaries, so a **1-core / 1 GB (even 512 MB) VPS installs without compiling anything**. On ultra-minimal images that ship without `curl` or even `sudo`, just run the installer as `root` — it pulls in what it needs.

### Method A: git clone (recommended, tracks the latest release)

```bash
# Fresh servers without git need it first (cloning requires it):
#   Debian / Ubuntu:  sudo apt-get update && sudo apt-get install -y git
#   RHEL family:      sudo dnf install -y git
git clone https://github.com/aipo-lenshow/EdgeNest.git
cd EdgeNest
sudo bash scripts/install.sh
```

By default the installer downloads a prebuilt artifact from the GitHub Release, falling back to a source build if none is available.

### Method B: install from a Release tarball (no git, no compile)

The tarball bundles the `edgenest` and `sing-box` binaries, which the installer reuses directly — skipping both the download and any on-host compile. Handy for low-memory machines or offline distribution.

```bash
VER=1.20.0626
ARCH=amd64   # use arm64 on ARM64 machines
curl -fsSL -O https://github.com/aipo-lenshow/EdgeNest/releases/download/v${VER}/edgenest-${VER}-linux-${ARCH}.tar.gz
tar -xzf edgenest-${VER}-linux-${ARCH}.tar.gz
cd edgenest-${VER}-linux-${ARCH}
sudo bash scripts/install.sh
```

### What the installer does

1. Lets you pick the panel language, then asks for the access host, panel port, and whether to add the Xray engine
2. Installs system dependencies and provisions sing-box (self-built with traffic statistics) plus the optional Xray engine
3. Creates the `edgenest.service` systemd unit, opens only the ports actually in use, and persists firewall rules
4. Enables BBR + fq congestion control (`--no-bbr` to skip)
5. Prints the panel URL, the initial username (`EdgeNest`), and a random password

For unattended installs use `sudo bash scripts/install.sh --yes` (all defaults); to uninstall, run `sudo bash scripts/uninstall.sh`, which cleans up fully and keeps your data by default.

### Managing from the server

After install, run **`edgenest`** on the server any time for a management menu — view the panel URL and admin account, restart / stop / start the service, tail live logs, reset the admin password, upgrade to the latest stable release, and uninstall. It's the quickest way back to the panel URL if you didn't bookmark it.

---

## Supported Platforms

| Category | Supported |
|---|---|
| Distributions | Debian · Ubuntu · CentOS · AlmaLinux · Rocky · Fedora |
| Architectures | x86_64 (amd64) · ARM64 (aarch64) |
| Privilege | root |

---

## Supported Protocols

| Engine | Inbound protocols |
|---|---|
| sing-box (default) | VLESS-Reality · VLESS-WS · VMess-WS · Trojan-TLS · Hysteria2 · TUIC v5 · Shadowsocks-2022 · AnyTLS · SOCKS5 |
| Xray (optional) | VLESS-XHTTP-Reality · VLESS-XHTTP-TLS |

Each inbound configures its own port, transport, and TLS certificate source (built-in self-signed or automatic ACME issuance). Protocols with WebSocket / XHTTP transports can layer on CDN and Argo tunnel access. The Xray engine is an optional install; without it the panel offers the sing-box protocols only.

---

## Panel Languages

The panel ships with 6 UI languages, chosen at install time and switchable any time from settings after login:

English · 简体中文 · 繁體中文 · فارسی (RTL) · Русский · Tiếng Việt

---

## Environment Variables

`install.sh` honors the following environment variables to override default behavior (command-line flags `--lang=` / `--yes` / `--no-bbr` / `--no-prebuilt` are also available):

| Variable | Default | Purpose |
|---|---|---|
| `EDGENEST_LANG` | detected from `$LANG` | Panel and installer language (`en` / `zh` / `zh-TW` / `fa` / `ru` / `vi`) |
| `EDGENEST_VERSION` | `1.20.0626` | Version used for the prebuilt artifact download |
| `EDGENEST_RELEASE_BASE` | GitHub Release download base | Base URL for prebuilt artifacts |
| `SINGBOX_VERSION` | `1.13.13` | sing-box version (always built with the `with_v2ray_api` traffic-stats tag) |
| `XRAY_VERSION` | `26.3.27` | Xray version (optional) |
| `GO_VERSION` | `1.26.0` | Used when a source build is needed and Go is absent |
| `NODE_MAJOR` | `20` | Used when a frontend source build is needed and Node is absent |

---

## Build From Source

```bash
make web      # build the frontend and embed it into the binary
make build    # single binary (frontend embedded)
./bin/edgenest --role standalone
```

Build requirements: Go 1.26+, Node 20+. `make release` cross-compiles linux/amd64 + linux/arm64 and produces tar.gz + SHA256SUMS. The sing-box proxy engine is self-built with the traffic-statistics tag via `scripts/build-singbox.sh`; the installer builds it on the spot when no prebuilt artifact is available.

---

## Acknowledgements

EdgeNest stands on these excellent open-source projects:

- [sing-box](https://github.com/SagerNet/sing-box) — core proxy engine
- [Xray-core](https://github.com/XTLS/Xray-core) — optional engine (VLESS-XHTTP)
- [utls](https://github.com/refraction-networking/utls) — TLS fingerprint mimicry
- [wireguard-go](https://github.com/WireGuard/wireguard-go) — WARP outbound foundation
- [lego](https://github.com/go-acme/lego) — ACME certificate issuance
- [cloudflared](https://github.com/cloudflare/cloudflared) — Argo tunnels

---

## License

[AGPL-3.0](./LICENSE).
