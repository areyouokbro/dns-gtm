#!/usr/bin/env bash
set -euo pipefail

# dns-gtm 管理脚本：交互式菜单 + 子命令两种方式
#   交互式：sudo ./dns-gtm.sh
#   子命令：sudo ./dns-gtm.sh install|uninstall|start|stop|restart|reload|status|edit
#
# 安装目录可用环境变量覆盖（解决 /usr/local/bin 只读/受限问题）：
#   BIN_DIR=/opt/gtm CONF_DIR=/opt/gtm/etc ./dns-gtm.sh install

BIN_NAME="dns-gtm"
SERVICE="dns-gtm.service"
BIN_DIR="${BIN_DIR:-/usr/local/bin}"
CONF_DIR="${CONF_DIR:-/etc/dns-gtm}"
BIN="$BIN_DIR/$BIN_NAME"
CONF="$CONF_DIR/config.yaml"
SERVICE_FILE="/etc/systemd/system/$SERVICE"
PID_FILE="/var/run/$BIN_NAME.pid"
LOG_FILE="/var/log/$BIN_NAME.log"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SOURCE_BIN="$SCRIPT_DIR/$BIN_NAME"
SOURCE_CONF="$SCRIPT_DIR/config.yaml"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'
info() { echo -e "${GREEN}[+]${NC} $*"; }
warn() { echo -e "${YELLOW}[!]${NC} $*"; }
err()  { echo -e "${RED}[-]${NC} $*" >&2; }

require_root() {
  if [[ $EUID -ne 0 ]]; then
    err "需要 root 权限，请用 sudo 运行：sudo $0"
    exit 1
  fi
}

has_systemd() {
  command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]]
}

# 检查目录是否可写（只读容器/受限文件系统会失败）
dir_writable() {
  local d="$1"
  if [[ -d "$d" ]]; then
    touch "$d/.wtest" 2>/dev/null && { rm -f "$d/.wtest"; return 0; } || return 1
  else
    # 目录不存在，尝试创建
    mkdir -p "$d" 2>/dev/null && touch "$d/.wtest" 2>/dev/null && { rm -f "$d/.wtest"; return 0; } || return 1
  fi
}

# ---------- nohup 兜底（无 systemd 时） ----------
start_nohup() {
  [[ -x "$BIN" ]] || { err "未安装（找不到 $BIN），先执行安装"; exit 1; }
  [[ -f "$CONF" ]] || { err "缺少配置 $CONF"; exit 1; }
  if [[ -f "$PID_FILE" ]] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
    warn "已在运行 (PID $(cat "$PID_FILE"))"
    return
  fi
  nohup "$BIN" -c "$CONF" >> "$LOG_FILE" 2>&1 &
  echo $! > "$PID_FILE"
  info "已启动 (PID $(cat "$PID_FILE"))，日志 $LOG_FILE"
}

stop_nohup() {
  if [[ -f "$PID_FILE" ]]; then
    local pid; pid="$(cat "$PID_FILE")"
    if kill -0 "$pid" 2>/dev/null; then
      kill "$pid" && info "已停止 (PID $pid)"
    fi
    rm -f "$PID_FILE"
  else
    warn "未运行"
  fi
}

# ---------- 无 systemd 的开机自启（crontab @reboot） ----------
setup_autostart() {
  local launcher="$CONF_DIR/autostart.sh"
  cat > "$launcher" <<EOF
#!/usr/bin/env bash
$BIN -c $CONF >> $LOG_FILE 2>&1 &
EOF
  chmod +x "$launcher"
  ( crontab -l 2>/dev/null | grep -v "$BIN_NAME"; echo "@reboot $launcher" ) | crontab -
  info "已设置开机自启（crontab @reboot）"
}

cleanup_autostart() {
  ( crontab -l 2>/dev/null | grep -v "$BIN_NAME" ) | crontab - 2>/dev/null || true
  rm -f "$CONF_DIR/autostart.sh"
}

# ---------- install ----------
do_install() {
  require_root
  [[ -f "$SOURCE_BIN" ]] || { err "未找到二进制 $SOURCE_BIN（请把 $BIN_NAME 与本脚本放同一目录）"; exit 1; }

  if ! dir_writable "$BIN_DIR"; then
    err "目录 $BIN_DIR 不可写（Operation not permitted / 只读文件系统）"
    err "请指定可写目录重新安装，例如："
    err "  BIN_DIR=/opt/gtm CONF_DIR=/opt/gtm/etc $0 install"
    exit 1
  fi

  info "安装二进制到 $BIN"
  install -m 0755 "$SOURCE_BIN" "$BIN"

  info "准备配置目录 $CONF_DIR"
  mkdir -p "$CONF_DIR"
  if [[ ! -f "$CONF" ]]; then
    if [[ -f "$SOURCE_CONF" ]]; then
      install -m 0644 "$SOURCE_CONF" "$CONF"
      info "已生成默认配置 $CONF"
    else
      err "缺少 config.yaml（请与本脚本放同一目录）"
      exit 1
    fi
  else
    warn "配置文件已存在，跳过（保留现有配置）"
  fi

  if has_systemd; then
    info "写入 systemd 服务 $SERVICE_FILE 并设置开机自启"
    cat > "$SERVICE_FILE" <<EOF
[Unit]
Description=DNS GTM
After=network.target

[Service]
ExecStart=$BIN -c $CONF
# 监听 53 特权端口时取消注释（免 root 绑定）：
# AmbientCapabilities=CAP_NET_BIND_SERVICE
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF
    systemctl daemon-reload
    systemctl enable --now "$SERVICE"
    info "已安装并启动，开机自启已启用："
    systemctl --no-pager --lines=0 status "$SERVICE" 2>/dev/null | head -n 5 || true
  else
    warn "未检测到 systemd，改用后台进程 + crontab 开机自启"
    start_nohup
    setup_autostart
  fi
  info "完成。配置热加载默认开启，改 $CONF 后自动生效。"
}

# ---------- uninstall ----------
do_uninstall() {
  require_root
  if has_systemd && [[ -f "$SERVICE_FILE" ]]; then
    systemctl disable --now "$SERVICE" 2>/dev/null || true
    rm -f "$SERVICE_FILE"
    systemctl daemon-reload
    info "已移除 systemd 服务与开机自启"
  fi
  cleanup_autostart
  if [[ -f "$PID_FILE" ]]; then
    local pid; pid="$(cat "$PID_FILE")"
    kill "$pid" 2>/dev/null || true
    rm -f "$PID_FILE"
  fi
  pkill -x "$BIN_NAME" 2>/dev/null || true
  rm -f "$BIN"
  info "已删除二进制 $BIN"
  if [[ "${KEEP_CONF:-0}" != "1" ]]; then
    rm -rf "$CONF_DIR"
    info "已删除配置目录 $CONF_DIR"
  else
    warn "按 KEEP_CONF=1 保留配置目录 $CONF_DIR"
  fi
  info "卸载完成"
}

# ---------- 生命周期 ----------
do_start() {
  require_root
  if has_systemd && [[ -f "$SERVICE_FILE" ]]; then
    systemctl start "$SERVICE" && info "已启动"
  else
    start_nohup
  fi
}

do_stop() {
  require_root
  if has_systemd && [[ -f "$SERVICE_FILE" ]]; then
    systemctl stop "$SERVICE" && info "已停止"
  else
    stop_nohup
  fi
}

do_restart() {
  require_root
  if has_systemd && [[ -f "$SERVICE_FILE" ]]; then
    systemctl restart "$SERVICE" && info "已重启"
  else
    stop_nohup
    start_nohup
  fi
}

do_reload() {
  require_root
  if has_systemd && [[ -f "$SERVICE_FILE" ]]; then
    systemctl reload "$SERVICE" 2>/dev/null || systemctl kill -s HUP "$SERVICE"
    info "已发送重载信号（SIGHUP）"
  elif [[ -f "$PID_FILE" ]] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
    kill -HUP "$(cat "$PID_FILE")" && info "已发送重载信号（SIGHUP）"
  else
    warn "未运行"
  fi
}

do_status() {
  if has_systemd && [[ -f "$SERVICE_FILE" ]]; then
    systemctl --no-pager status "$SERVICE" || true
  elif [[ -f "$PID_FILE" ]] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
    info "运行中 (PID $(cat "$PID_FILE"))"
  else
    warn "未运行"
  fi
}

# ---------- 编辑配置 ----------
do_edit() {
  require_root
  [[ -f "$CONF" ]] || { err "配置不存在：$CONF"; exit 1; }
  local editor="${EDITOR:-}"
  if [[ -z "$editor" ]]; then
    editor="$(command -v vim || command -v vi || command -v nano || true)"
  fi
  [[ -z "$editor" ]] && { err "未找到编辑器（vim/vi/nano）"; exit 1; }
  "$editor" "$CONF"
  info "配置已保存，$CONF 数秒内自动生效。"
  do_reload
}

# ---------- 状态判断 ----------
is_installed() { [[ -x "$BIN" ]]; }

is_running() {
  if has_systemd && [[ -f "$SERVICE_FILE" ]]; then
    systemctl is-active --quiet "$SERVICE" 2>/dev/null
  else
    [[ -f "$PID_FILE" ]] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null
  fi
}

status_text() {
  if ! is_installed; then echo "未安装"; return; fi
  if is_running; then echo "运行中"; else echo "已停止"; fi
}

# ---------- 交互式菜单 ----------
show_menu() {
  echo
  echo -e "  ${CYAN}==================== DNS GTM 管理 ====================${NC}"
  printf  "  ${YELLOW}状态: %s${NC}\n" "$(status_text)"
  echo    "  ----------------------------------------------------"
  echo    "    1) 安装        install"
  echo    "    2) 卸载        uninstall"
  echo    "    3) 启动        start"
  echo    "    4) 关闭        stop"
  echo    "    5) 重启        restart"
  echo    "    6) 重载配置    reload"
  echo    "    7) 查看状态    status"
  echo    "    8) 编辑配置    edit"
  echo    "    0) 退出"
  echo    "  ===================================================="
}

menu_loop() {
  while true; do
    show_menu
    local choice
    read -r -p "  请选择 [0-8]: " choice
    echo
    case "$choice" in
      1) do_install ;;
      2) do_uninstall ;;
      3) do_start ;;
      4) do_stop ;;
      5) do_restart ;;
      6) do_reload ;;
      7) do_status ;;
      8) do_edit ;;
      0|q|Q) info "已退出"; break ;;
      *) warn "无效选择：$choice" ;;
    esac
    echo
    read -r -p "  按回车返回菜单..." _
  done
}

usage() {
  cat <<'EOF'
用法: dns-gtm.sh [命令]

不带参数运行进入交互式菜单；带命令则直接执行。

命令:
  install    安装（二进制 + 配置 + systemd 服务 + 开机自启）
  uninstall  卸载（停止服务、删除二进制与配置、移除自启）
  start      启动
  stop       关闭
  restart    重启
  reload     重载配置（SIGHUP）
  status     查看状态
  edit       编辑配置文件（改完自动生效）
  help       显示本帮助

环境变量:
  BIN_DIR     二进制安装目录（默认 /usr/local/bin）
  CONF_DIR    配置目录（默认 /etc/dns-gtm）
  KEEP_CONF=1 卸载时保留配置目录

示例:
  sudo ./dns-gtm.sh                      # 交互式菜单
  sudo ./dns-gtm.sh install
  # 目录受限时（如容器/只读 /usr）：
  sudo BIN_DIR=/opt/gtm CONF_DIR=/opt/gtm/etc ./dns-gtm.sh install
EOF
}

# ---------- 入口 ----------
if [[ $# -eq 0 ]]; then
  require_root
  menu_loop
else
  case "$1" in
    install)   do_install ;;
    uninstall) do_uninstall ;;
    start)     do_start ;;
    stop)      do_stop ;;
    restart)   do_restart ;;
    reload)    do_reload ;;
    status)    do_status ;;
    edit)      do_edit ;;
    help|-h|--help) usage ;;
    *) usage; exit 1 ;;
  esac
fi
