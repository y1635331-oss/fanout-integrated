#!/usr/bin/env bash
set -euo pipefail
base=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
fixture=$(mktemp -d)
trap 'rm -rf -- "$fixture"' EXIT
mkdir -p "$fixture/bin" "$fixture/package/fanout-integrated"
printf '#!/bin/bash\nprintf installed > "$FANOUT_FIXTURE/installed"\n' > "$fixture/package/fanout-integrated/install.sh"
tar -czf "$fixture/fanout-integrated-v9.9.9.tar.gz" -C "$fixture/package" fanout-integrated
(cd "$fixture" && sha256sum fanout-integrated-v9.9.9.tar.gz > SHA256SUMS-v9.9.9.txt)
cat > "$fixture/bin/curl" <<'MOCK'
#!/bin/bash
set -eu
dest='';url=''
while (($#)); do
 case "$1" in
  -o) dest=$2;shift 2;;
  --proto|--proto-redir|--retry|--connect-timeout|--max-time|-w) shift 2;;
  https://*) url=$1;shift;;
  *) shift;;
 esac
done
[[ ${FANOUT_FAIL_DOWNLOAD:-0} != 1 ]] || exit 22
if [[ "$url" == */latest ]]; then
 printf https://github.com/y1635331-oss/fanout-integrated/releases/tag/v9.9.9
else
 cp "$FANOUT_FIXTURE/${url##*/}" "$dest"
fi
MOCK
chmod +x "$fixture/bin/curl"
export FANOUT_FIXTURE="$fixture"
export PATH="$fixture/bin:$PATH"
bash "$base/bootstrap.sh"
[[ $(cat "$fixture/installed") == installed ]]
# Windows-generated checksum manifests must verify without disabling hashes.
sed 's/$/\r/' "$fixture/SHA256SUMS-v9.9.9.txt" > "$fixture/crlf.txt"
mv "$fixture/crlf.txt" "$fixture/SHA256SUMS-v9.9.9.txt"
bash "$base/bootstrap.sh"
[[ $(cat "$fixture/installed") == installed ]]

rm "$fixture/installed"
printf 'corrupt' >> "$fixture/fanout-integrated-v9.9.9.tar.gz"
if bash "$base/bootstrap.sh"; then echo 'ERROR: corrupted archive accepted';exit 1;fi
[[ ! -f "$fixture/installed" ]]
: > "$fixture/SHA256SUMS-v9.9.9.txt"
if bash "$base/bootstrap.sh"; then echo 'ERROR: empty checksum accepted';exit 1;fi
if FANOUT_FAIL_DOWNLOAD=1 bash "$base/bootstrap.sh"; then echo 'ERROR: failed download accepted';exit 1;fi
[[ ! -f "$fixture/installed" ]]
echo 'Bootstrap verified: valid install, corrupt/missing checksum and network failure.'
