#!/usr/bin/env bash
set -Eeuo pipefail
failed_line=unknown
trap 'failed_line=$LINENO' ERR
cd -- "$(dirname -- "${BASH_SOURCE[0]}")"
[[ $EUID -eq 0 ]] || { echo '请使用 sudo bash install.sh'; exit 1; }
exec 9>/run/fanout-integrated-install.lock
flock -n 9 || { echo '另一个安装或域名配置任务正在运行';exit 1; }
[[ -f /etc/os-release ]] || { echo '需要 Ubuntu/Debian'; exit 1; }
. /etc/os-release
case "$ID ${ID_LIKE:-}" in *ubuntu*|*debian*) ;; *) echo '此安装包支持 Ubuntu/Debian'; exit 1;; esac
case "$(uname -m)" in x86_64) arch=amd64;; aarch64|arm64) arch=arm64;; *) echo '仅提供 amd64 / arm64';exit 1;; esac
binary="bin/fanout-linux-$arch"
if [[ ! -f "$binary" ]]; then
  command -v go >/dev/null || { echo '缺少预编译程序，请使用完整安装包，或安装 Go 1.24+ 后从源码编译';exit 1; }
  mkdir -p bin
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags='-s -w' -o "$binary" .
else
  [[ -f BIN_SHA256SUMS ]] || { echo '缺少程序校验和';exit 1; }
  grep -E "  bin/fanout-linux-${arch}$" BIN_SHA256SUMS | sha256sum -c -
fi
[[ -c /dev/net/tun ]] || { echo 'VPS 未提供 /dev/net/tun，请先在主机商处启用 TUN';exit 1; }
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y openvpn iproute2 iptables curl ca-certificates openssl python3
openvpn_version=$(openvpn --version)
[[ "$openvpn_version" =~ OpenVPN\ 2\.[5-9] || "$openvpn_version" =~ OpenVPN\ [3-9]\. ]] || { echo '需要 OpenVPN 2.5+（建议 Ubuntu 22.04+ / Debian 12+）';exit 1; }
WEB_PORT=${WEB_PORT:-8899}
if [[ -z ${MAX_EXITS:-} && -f /etc/systemd/system/fanout-integrated.service ]]; then
  MAX_EXITS=$(sed -nE 's/^ExecStart=.* -max ([0-9]+)( .*)?$/\1/p' /etc/systemd/system/fanout-integrated.service)
fi
MAX_EXITS=${MAX_EXITS:-20}
[[ "$WEB_PORT" =~ ^[0-9]+$ && "$MAX_EXITS" =~ ^[0-9]+$ ]] || { echo 'WEB_PORT / MAX_EXITS 必须是数字';exit 1; }
((WEB_PORT>=1 && WEB_PORT<=65535 && MAX_EXITS>=1 && MAX_EXITS<=254)) || { echo '端口范围 1-65535，出口数量 1-254';exit 1; }
DATA=/var/lib/fanout-integrated
mkdir -p "$DATA"
chmod 700 "$DATA"
if [[ -f "$DATA/settings.json" ]]; then
  echo '升级保留现有面板端口和设置。'
else
  if ss -ltnH | awk '{print $4}' | grep -Eq ":${WEB_PORT}$"; then
    echo "端口 $WEB_PORT 已被占用，请使用 sudo WEB_PORT=其他端口 bash install.sh";exit 1
  fi
  printf '{"port":%s,"listen_addr":""}\n' "$WEB_PORT" > "$DATA/settings.json"
  chmod 600 "$DATA/settings.json"
fi
# Download and checks have already completed; keep a root-only recovery snapshot.
backup=$(mktemp -d /var/lib/fanout-integrated-backup.XXXXXXXX)
chmod 700 "$backup"
had_binary=0
if [[ -f /usr/local/bin/fanout-integrated ]]; then
  had_binary=1
  cp -a /usr/local/bin/fanout-integrated "$backup/program"
fi
for file in /etc/systemd/system/fanout-integrated.service /usr/local/bin/fanoutctl; do
  [[ ! -f "$file" ]] || cp -a "$file" "$backup/"
done
recover() {
  result=$?
  trap - EXIT
  if ((result != 0 && had_binary == 1)); then
    echo "更新失败（安装脚本第 $failed_line 行），正在恢复原程序；配置备份：$backup"
    systemctl stop fanout-integrated.service 2>/dev/null || true
    install -m 755 "$backup/program" /usr/local/bin/fanout-integrated
    [[ ! -f "$backup/fanout-integrated.service" ]] || cp -a "$backup/fanout-integrated.service" /etc/systemd/system/fanout-integrated.service
    [[ ! -f "$backup/fanoutctl" ]] || cp -a "$backup/fanoutctl" /usr/local/bin/fanoutctl
    systemctl daemon-reload
    systemctl start fanout-integrated.service || true
  fi
  exit "$result"
}
trap recover EXIT
if [[ -f /etc/systemd/system/fanout-integrated.service ]]; then systemctl stop fanout-integrated.service; fi
cp -a "$DATA" "$backup/data"
if [[ -f /usr/local/bin/fanout-integrated ]]; then
  install -m 755 /usr/local/bin/fanout-integrated /usr/local/bin/fanout-integrated.previous
fi
install -m 755 "$binary" /usr/local/bin/fanout-integrated.new
mv -f /usr/local/bin/fanout-integrated.new /usr/local/bin/fanout-integrated
install -m 755 f.sh /usr/local/bin/fanoutctl
install -d -m 755 /usr/local/lib/fanout-integrated
install -m 755 core-install.sh /usr/local/lib/fanout-integrated/core-install.sh
install -m 755 bootstrap.sh /usr/local/lib/fanout-integrated/bootstrap.sh
install -m 755 panel-config.py /usr/local/lib/fanout-integrated/panel-config.py
cat > /etc/systemd/system/fanout-integrated.service <<EOF
[Unit]
Description=Fanout Integrated SOCKS5 Gateway
After=network-online.target
Wants=network-online.target
[Service]
Type=simple
EnvironmentFile=-/etc/default/fanout-integrated
ExecStart=/usr/local/bin/fanout-integrated -dir /var/lib/fanout-integrated -max $MAX_EXITS
Restart=on-failure
RestartSec=5
TimeoutStopSec=90
KillMode=mixed
UMask=0077
LimitNOFILE=65536
[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now fanout-integrated.service
echo '正在等待面板就绪…'
ready=0
for _ in $(seq 1 75); do
  if [[ -f "$DATA/password" && -f "$DATA/basepath" ]]; then
    if python3 /usr/local/lib/fanout-integrated/panel-config.py health; then ready=1;break;fi
  fi
  sleep 1
done
[[ $ready == 1 ]] && systemctl is-active --quiet fanout-integrated.service || { journalctl -u fanout-integrated -n 30 --no-pager;exit 1; }
trap - EXIT
echo "安装完成。更新前的配置备份：$backup"
fanoutctl info
echo '请按云安全组规则放行管理端口和实际使用的 SOCKS5 端口。'
echo '直接生成 S5 不需要 Xray。高级节点功能可复用现有 3x-ui / Xray。'
