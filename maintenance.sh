#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
DATA=/var/lib/fanout-integrated
LIB=/usr/local/lib/fanout-integrated
action=${1:-}
case "$action" in app-update|core-install) ;; *) echo '无效维护操作';exit 2;; esac
[[ $EUID -eq 0 ]] || exit 1
mkdir -p "$DATA"
# A separate systemd unit survives the application restart during an update.
# Parse the whole function before install.sh replaces this script on disk.
main() {
 exec 8>/run/fanout-maintenance.lock
 flock -n 8 || { echo '维护任务已在运行';return 1; }
 exec >"$DATA/maintenance.log" 2>&1
 printf '{"status":"running","action":"%s"}\n' "$action" > "$DATA/maintenance.json.tmp"
 mv "$DATA/maintenance.json.tmp" "$DATA/maintenance.json"
 finish() {
  code=$?
  trap - EXIT
  status=success;[[ $code == 0 ]] || status=failed
  printf '{"status":"%s","action":"%s","exit_code":%d,"finished":"%s"}\n' "$status" "$action" "$code" "$(date -u +%FT%TZ)" > "$DATA/maintenance.json.tmp"
  mv "$DATA/maintenance.json.tmp" "$DATA/maintenance.json"
  exit "$code"
 }
 trap finish EXIT
 export FANOUT_NONINTERACTIVE=1
 echo "开始维护：$action"
 if [[ "$action" == app-update ]];then
  bash "$LIB/bootstrap.sh" update
 else
  bash "$LIB/core-install.sh"
 fi
 echo '维护完成；内核更新后，新建或重连的出口使用新版内核。'
}
main
