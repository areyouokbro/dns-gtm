#!/usr/bin/env bash
set -euo pipefail

# dns-gtm 一键安装脚本
# 用法：
#   curl -fsSL https://raw.githubusercontent.com/areyouokbro/dns-gtm/main/install.sh | sudo bash
#   wget -qO- https://raw.githubusercontent.com/areyouokbro/dns-gtm/main/install.sh | sudo bash
#
# 从 GitHub Releases 下载对应架构（amd64/arm64）静态二进制，自动安装并配置 systemd 开机自启。
# 完成后用「sudo dns-gtm.sh」进行日常管理。
# 目录受限的容器环境可覆盖安装目录：BIN_DIR=/opt/gtm CONF_DIR=/opt/gtm/etc bash install.sh

REPO="areyouokbro/dns-gtm"
RAW="https://raw.githubusercontent.com/$REPO/main"
DL="https://github.com/$REPO/releases/latest/download"

BIN_DIR="${BIN_DIR:-/usr/local/bin}"
CONF_DIR="${CONF_DIR:-/etc/dns-gtm}"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'
info() { echo -e "${GREEN}[+]${NC} $*"; }
warn() { echo -e "${YELLOW}[!]${NC} $*"; }
err()  { echo -e "${RED}[-]${NC} $*" >&2; }

require_root() {
  if [[ $EUID -ne 0 ]]; then
    err "需要 root 权限，请用：curl -fsSL .../install.sh | sudo bash"
    exit 1
  fi
}

detect_arch() {
  local m; m="$(uname -m)"
  case "$m" in
    x86_64|amd64)  echo "amd64" ;;
    aarch64|arm64) echo "arm64" ;;
    *) err "不支持的架构: $m（仅支持 amd64 / arm64）"; exit 1 ;;
  esac
}

main() {
  require_root
  command -v curl >/dev/null 2>&1 || { err "需要 curl，请先安装（apt install curl / yum install curl）"; exit 1; }

  local arch; arch="$(detect_arch)"
  info "检测到架构: $arch"

  local tmp; tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT

  info "下载 dns-gtm 二进制 (linux-$arch)..."
  curl -fL --progress-bar -o "$tmp/dns-gtm" "$DL/dns-gtm-linux-$arch"

  info "下载管理脚本与配置模板..."
  curl -fsSL -o "$tmp/dns-gtm.sh" "$RAW/dns-gtm.sh"
  curl -fsSL -o "$tmp/config.yaml" "$RAW/config.yaml"
  chmod +x "$tmp/dns-gtm" "$tmp/dns-gtm.sh"

  info "执行安装（二进制 + 配置 + systemd + 开机自启）..."
  BIN_DIR="$BIN_DIR" CONF_DIR="$CONF_DIR" bash "$tmp/dns-gtm.sh" install

  # 把管理脚本装进 PATH，方便日后管理
  install -m 0755 "$tmp/dns-gtm.sh" "$BIN_DIR/dns-gtm.sh"

  echo
  info "一键安装完成！"
  echo -e "  ${CYAN}管理命令:${NC} sudo dns-gtm.sh                # 交互式菜单"
  echo -e "  ${CYAN}配置目录:${NC} $CONF_DIR/config.yaml         # 改完自动热加载"
  echo -e "  ${CYAN}测试解析:${NC} dig @127.0.0.1 -p 5353 <域名> A +short"
}

main "$@"
