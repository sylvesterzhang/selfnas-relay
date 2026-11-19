#!/usr/bin/env bash
# selfnas-relay 服务端管理脚本
# 用法：./start.sh {start|stop|restart|status}
# 解压后放在和 relay 二进制同一目录使用。

set -u

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN="$DIR/relay"
PIDFILE="$DIR/relay.pid"
LOGFILE="$DIR/relay.log"
ENVFILE="$DIR/.env"

# 校验 PID 文件里的进程确实是本目录的 relay，避免 PID 复用误杀别的程序。
running_pid() {
  [ -f "$PIDFILE" ] || return 1
  local pid
  pid="$(cat "$PIDFILE" 2>/dev/null)"
  [ -n "$pid" ] || return 1
  if [ -r "/proc/$pid/cmdline" ] && tr '\0' ' ' < "/proc/$pid/cmdline" 2>/dev/null | grep -qF "$BIN"; then
    echo "$pid"
    return 0
  fi
  return 1
}

# 程序本身不读 .env 文件，只认环境变量；这里把 .env 里的 KEY=VALUE 导出给进程。
load_env() {
  if [ -f "$ENVFILE" ]; then
    set -a
    # shellcheck disable=SC1090
    . "$ENVFILE"
    set +a
  fi
}

cmd_start() {
  local pid
  if pid="$(running_pid)"; then
    echo "selfnas-relay 已在运行 (PID $pid)"
    return 0
  fi
  if [ ! -f "$BIN" ]; then
    echo "找不到 relay 二进制：$BIN"
    return 1
  fi
  chmod +x "$BIN"
  load_env
  cd "$DIR" || return 1
  nohup "$BIN" >>"$LOGFILE" 2>&1 &
  echo $! >"$PIDFILE"
  sleep 1
  if pid="$(running_pid)"; then
    echo "selfnas-relay 已启动 (PID $pid)"
    echo "日志：$LOGFILE"
    echo "管理台：http://127.0.0.1:${RELAY_LISTEN_PORT:-8080}/"
  else
    echo "启动失败，请查看日志：$LOGFILE"
    rm -f "$PIDFILE"
    return 1
  fi
}

cmd_stop() {
  local pid
  if ! pid="$(running_pid)"; then
    echo "selfnas-relay 未在运行"
    rm -f "$PIDFILE"
    return 0
  fi
  kill "$pid" 2>/dev/null
  local i=0
  while kill -0 "$pid" 2>/dev/null && [ "$i" -lt 20 ]; do
    sleep 0.2
    i=$((i + 1))
  done
  if kill -0 "$pid" 2>/dev/null; then
    echo "进程未在 4 秒内退出，发送 SIGKILL"
    kill -9 "$pid" 2>/dev/null
  fi
  rm -f "$PIDFILE"
  echo "selfnas-relay 已停止 (PID $pid)"
}

cmd_status() {
  local pid
  if pid="$(running_pid)"; then
    echo "selfnas-relay 运行中 (PID $pid)"
  else
    echo "selfnas-relay 未运行"
  fi
  if [ -f "$LOGFILE" ]; then
    echo "日志文件：$LOGFILE"
  fi
}

case "${1:-}" in
  start) cmd_start ;;
  stop) cmd_stop ;;
  restart) cmd_stop; cmd_start ;;
  status) cmd_status ;;
  *)
    echo "用法：$0 {start|stop|restart|status}"
    exit 1
    ;;
esac
