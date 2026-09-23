#!/usr/bin/env bash
set -euo pipefail
DATA=/var/lib/fanout-integrated
case "${1:-info}" in
 core-install) exec bash /usr/local/lib/fanout-integrated/core-install.sh;;
 domain|cert-check|proxy)
   exec python3 /usr/local/lib/fanout-integrated/panel-config.py "$@";;
 info)
   python3 /usr/local/lib/fanout-integrated/panel-config.py info
   exit 0
   ;;
 start|stop|restart|status) systemctl "$1" fanout-integrated.service;;
 log) journalctl -u fanout-integrated -n 100 --no-pager;;
 password)
   read -r -s -p '输入新口令（至少12位）：' password;printf '\n'
   ((${#password}>=12)) || { echo '口令太短';exit 1; }
   umask 077;printf '%s\n' "$password" > "$DATA/password.tmp";mv "$DATA/password.tmp" "$DATA/password"
   systemctl restart fanout-integrated.service;;
 rollback)
   [[ -f /usr/local/bin/fanout-integrated.previous ]] || { echo '没有旧版备份';exit 1; }
   systemctl stop fanout-integrated.service
   install -m 755 /usr/local/bin/fanout-integrated.previous /usr/local/bin/fanout-integrated
   systemctl start fanout-integrated.service;;
 update)
   [[ $EUID -eq 0 ]] || { echo '请使用 sudo fanoutctl update';exit 1; }
   exec bash /usr/local/lib/fanout-integrated/bootstrap.sh update;;
 *) echo '用法：fanoutctl info|start|stop|restart|status|log|password|rollback|update|domain|cert-check|proxy|core-install';exit 1;;
esac
