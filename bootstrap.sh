#!/usr/bin/env bash
set -euo pipefail
umask 077
REPO='https://github.com/y1635331-oss/fanout-integrated'
mode=${1:-install}
case "$mode" in install|update) ;; *) echo '用法：bash bootstrap.sh [install|update]'; exit 2;; esac
[[ $EUID -eq 0 ]] || { echo '请使用 sudo bash 执行安装或更新'; exit 1; }
if [[ "$mode" == update && ! -x /usr/local/bin/fanout-integrated ]]; then
  echo '尚未安装，请先运行一键安装命令'; exit 1
fi
for tool in curl tar sha256sum awk mktemp; do
  command -v "$tool" >/dev/null || { echo "缺少 $tool，请先安装 curl、ca-certificates、tar、coreutils"; exit 1; }
done
scratch=$(mktemp -d -t fanout-install.XXXXXXXX)
trap 'rm -rf -- "$scratch"' EXIT
echo '正在获取综合版最新正式版本…'
latest=$(curl --proto '=https' --proto-redir '=https' -fsSL --retry 3 --connect-timeout 15 --max-time 120 -o /dev/null -w '%{url_effective}' "$REPO/releases/latest")
tag=${latest##*/}
[[ "$latest" == "$REPO/releases/tag/$tag" && "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo '无法确定最新正式版本，未修改已安装程序'; exit 1; }
asset="fanout-integrated-$tag.tar.gz"
sum="SHA256SUMS-$tag.txt"
fetch() { curl --proto '=https' --proto-redir '=https' -fSL --retry 3 --connect-timeout 15 --max-time 600 "$1" -o "$2"; }
echo "正在下载 $tag…"
fetch "$REPO/releases/download/$tag/$asset" "$scratch/$asset"
fetch "$REPO/releases/download/$tag/$sum" "$scratch/$sum"
cd -- "$scratch"
awk -v name="$asset" '{sub(/\r$/, ""); if ($2 == name) print}' "$sum" > selected.sha256
[[ $(wc -l < selected.sha256) -eq 1 ]] || { echo '校验文件缺失或重复，已停止'; exit 1; }
sha256sum --strict -c selected.sha256
tar --no-same-owner --no-same-permissions -xzf "$asset"
[[ -f fanout-integrated/install.sh ]] || { echo '安装包结构不正确'; exit 1; }
echo '下载与校验完成，开始安装。已有配置会保留，更新过程中服务会短暂重启。'
bash fanout-integrated/install.sh
echo "已完成 $tag。以后可执行：sudo fanoutctl update"
