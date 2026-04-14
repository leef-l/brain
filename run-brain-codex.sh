#!/usr/bin/env bash
set -euo pipefail

SESSION_NAME="${SESSION_NAME:-brain_codex}"
WORKDIR="/www/wwwroot/project/exchange/codex/brain"
LOG_DIR="${WORKDIR}/.tmp"
LOG_FILE="${LOG_DIR}/brain_codex.log"
PROMPT_FILE="${LOG_DIR}/brain_codex.prompt.txt"
RESTART_DELAY="${RESTART_DELAY:-5}"
LOG_TAIL_LINES="${LOG_TAIL_LINES:-120}"

mkdir -p "${LOG_DIR}"

write_prompt() {
  cat > "${PROMPT_FILE}" <<'EOF'
开始 agent teams 工作，目标是把当前“量化系统三脑”推进到生产级别。

当前结论：
- 当前系统还没有达到生产级别。

执行要求：
1. 默认直接执行，不要停留在泛泛分析。
2. 持续推进架构梳理、代码修复、文档补齐、脚本完善、风险收敛和交付清单更新。
3. 每完成一个可验证子任务，立即输出一次进度汇报，格式固定为：
4. 如果没有达到生产级别，就开始下一轮.

[进度汇报]
- 已完成：
- 修改位置：
- 当前风险：
- 下一步：

4. 遇到问题先自查、自修、自推进，不要因为单点问题停止。
5. 只有遇到明确外部阻塞时，才允许汇报阻塞点；除外部阻塞外不要停止。
6. 所有结论必须落到代码、脚本、文档或明确待办，禁止空转。
7. 始终在目录 /www/wwwroot/project/exchange/codex/brain 下工作。
8. 如果上一次会话未完成，优先继续上一次会话；如果没有历史会话，再按本提示词从头开始。
EOF
}

sanitize_stream() {
  perl -ne 's/\e(?:[@-Z\\-_]|\[[0-?]*[ -\/]*[@-~]|\][^\a]*(?:\a|\e\\))//g; s/\r/\n/g; s/0;[^\n]*brain//g; next if /^\s*$/; print'
}

show_log_snapshot_from_file() {
  sanitize_stream < "${LOG_FILE}" \
    | perl -ne 'next unless /(?:\p{Han}|[A-Za-z0-9]).{20,}/; print' \
    | tail -n "${LOG_TAIL_LINES}"
}

start_session() {
  if tmux has-session -t "${SESSION_NAME}" 2>/dev/null; then
    echo "tmux session already exists: ${SESSION_NAME}"
    exit 0
  fi

  write_prompt

  tmux new-session -d -s "${SESSION_NAME}" -c "${WORKDIR}" \
    "bash -lc '
      mkdir -p \"${LOG_DIR}\"
      touch \"${LOG_FILE}\"
      while true; do
        codex resume --last \
          --dangerously-bypass-approvals-and-sandbox \
          --search \
          --no-alt-screen \
          -C \"${WORKDIR}\" \
          \"继续当前任务。若未完成，继续推进，并按约定输出进度汇报。\" \
        || codex \
          --dangerously-bypass-approvals-and-sandbox \
          --search \
          --no-alt-screen \
          -C \"${WORKDIR}\" \
          \"\$(cat \"${PROMPT_FILE}\")\"
        printf \"[codex exited at %s] restart in ${RESTART_DELAY}s\n\" \"\$(date \"+%F %T\")\"
        sleep ${RESTART_DELAY}
      done
    '"

  tmux set-option -t "${SESSION_NAME}" remain-on-exit on >/dev/null
  tmux set-option -t "${SESSION_NAME}" mouse on >/dev/null
  tmux pipe-pane -o -t "${SESSION_NAME}" "cat >> '${LOG_FILE}'"

  echo "started session: ${SESSION_NAME}"
  echo "workdir: ${WORKDIR}"
  echo "log: ${LOG_FILE}"
}

attach_session() {
  if ! tmux has-session -t "${SESSION_NAME}" 2>/dev/null; then
    echo "session not found: ${SESSION_NAME}"
    exit 1
  fi
  tmux attach -t "${SESSION_NAME}"
}

show_logs() {
  local follow="${1:-}"
  touch "${LOG_FILE}"
  if [[ "${follow}" == "-f" || "${follow}" == "--follow" ]]; then
    tail -n "${LOG_TAIL_LINES}" -F "${LOG_FILE}" | sanitize_stream
    return
  fi

  if tmux has-session -t "${SESSION_NAME}" 2>/dev/null; then
    tmux capture-pane -p -J -t "${SESSION_NAME}" -S "-${LOG_TAIL_LINES}"
    return
  fi

  show_log_snapshot_from_file
}

show_status() {
  if ! tmux has-session -t "${SESSION_NAME}" 2>/dev/null; then
    echo "session not found: ${SESSION_NAME}"
    echo "log: ${LOG_FILE}"
    exit 1
  fi
  echo "session: ${SESSION_NAME}"
  echo "log: ${LOG_FILE}"
  tmux list-panes -t "${SESSION_NAME}" -F '#{session_name} #{pane_pid} #{pane_current_command} #{pane_dead}'
}

stop_session() {
  tmux kill-session -t "${SESSION_NAME}" 2>/dev/null || true
  echo "stopped session: ${SESSION_NAME}"
}

restart_session() {
  stop_session
  start_session
}

usage() {
  cat <<EOF
Usage: $(basename "$0") <command>

Commands:
  start    start tmux codex session with widest local permissions
  attach   attach to tmux session
  logs     show recent logs and exit; use -f to follow
  status   show tmux session status
  stop     stop tmux session
  restart  restart tmux session
EOF
}

cmd="${1:-start}"
if [[ $# -gt 0 ]]; then
  shift
fi

case "${cmd}" in
  start)
    start_session
    ;;
  attach)
    attach_session
    ;;
  logs)
    show_logs "$@"
    ;;
  status)
    show_status
    ;;
  stop)
    stop_session
    ;;
  restart)
    restart_session
    ;;
  *)
    usage
    exit 1
    ;;
esac
