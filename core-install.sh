#!/usr/bin/env bash
set -euo pipefail
[[ $EUID -eq 0 ]] || { echo '请使用 sudo fanoutctl core-install'; exit 1; }
exec 9>/run/fanout-integrated-install.lock
flock -n 9 || { echo '另一个安装任务正在运行'; exit 1; }
case "$(uname -m)" in
 x86_64) arch=amd64; hash=12cb2816b52febb356f6a885b740cc8758c3f30b8ae0ca8edba80f0d2d35343f;;
 aarch64|arm64) arch=arm64; hash=6060b42fa84c5dcaeae1799af7f61b0f1ae4855d9d5ddc9e02baba17154b3ae2;;
 *) echo '仅支持 amd64/arm64'; exit 1;;
esac
umask 077
tmp=$(mktemp -d)
trap 'rm -rf -- "$tmp"' EXIT
name="sing-box-1.14.1-linux-$arch"
curl --proto '=https' --proto-redir '=https' -fL --retry 3 --connect-timeout 15 --max-time 300 "https://github.com/SagerNet/sing-box/releases/download/v1.14.1/$name.tar.gz" -o "$tmp/core.tar.gz"
printf '%s  %s
' "$hash" "$tmp/core.tar.gz" | sha256sum -c -
tar -xzf "$tmp/core.tar.gz" -C "$tmp" --no-same-owner --no-same-permissions
"$tmp/$name/sing-box" version
install -d -m 755 /usr/local/lib/fanout-integrated
install -m 755 "$tmp/$name/sing-box" /usr/local/lib/fanout-integrated/sing-box.new
mv -f /usr/local/lib/fanout-integrated/sing-box.new /usr/local/lib/fanout-integrated/sing-box
echo '订阅内核安装完成。可在面板添加多协议订阅并建立出口。'
