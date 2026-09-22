#!/usr/bin/env bash
set -euo pipefail
DATA=/var/lib/fanout-integrated
case "${1:-info}" in
 info)
   [[ -f "$DATA/password" ]] || { echo '尚未初始化，请检查 fanoutctl log';exit 1; }
   host=$(curl -4fsS --max-time 5 https://api.ipify.org || printf '<VPS公网IP>')
   port=$(sed -nE 's/.*"port"[[:space:]]*:[[:space:]]*([0-9]+).*/\1/p' "$DATA/settings.json")
   base=$(cat "$DATA/basepath")
   printf '管理地址：https://%s:%s%s/\n' "$host" "${port:-8899}" "$base"
   printf '管理口令：';cat "$DATA/password";printf '\n'
   if [[ -f "$DATA/web.crt" ]]; then openssl x509 -in "$DATA/web.crt" -noout -fingerprint -sha256;fi
   echo '首次使用默认自签证书，请通过 SSH 输出核对证书指纹后信任；也可配置自己的域名证书。';;
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
 update) echo '请下载综合版完整安装包，校验后执行 bash install.sh。不会从原版仓库覆盖。';;
 *) echo '用法：fanoutctl info|start|stop|restart|status|log|password|rollback|update';exit 1;;
esac
