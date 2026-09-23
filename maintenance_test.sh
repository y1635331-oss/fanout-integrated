#!/usr/bin/env bash
set -euo pipefail
base=$(cd -- "$(dirname -- "$0")" && pwd)
fixture=$(mktemp -d)
trap 'rm -rf -- "$fixture"' EXIT
mkdir -p "$fixture/lib" "$fixture/data"
python3 - "$base/maintenance.sh" "$fixture" <<'PY'
from pathlib import Path
import sys
source=Path(sys.argv[1]).read_text()
root=Path(sys.argv[2])
source=source.replace('/var/lib/fanout-integrated',str(root/'data')).replace('/usr/local/lib/fanout-integrated',str(root/'lib')).replace('/run/fanout-maintenance.lock',str(root/'lock'))
(root/'runner.sh').write_text(source)
PY
cat > "$fixture/lib/bootstrap.sh" <<'MOCK'
#!/bin/bash
set -eu
[[ $FANOUT_NONINTERACTIVE == 1 ]]
echo 'download verified, installed'
MOCK
bash "$fixture/runner.sh" app-update
python3 - "$fixture/data/maintenance.json" <<'PY'
import json,sys
s=json.load(open(sys.argv[1]))
assert s['status']=='success' and s['action']=='app-update'
PY
printf '#!/bin/bash\necho "simulated download failure"\nexit 7\n' > "$fixture/lib/core-install.sh"
if bash "$fixture/runner.sh" core-install;then echo 'failure incorrectly accepted';exit 1;fi
python3 - "$fixture/data/maintenance.json" <<'PY'
import json,sys
s=json.load(open(sys.argv[1]))
assert s['status']=='failed' and s['exit_code']==7
PY
if bash "$fixture/runner.sh" 'app-update; echo injected';then exit 1;fi
echo 'persistent maintenance success/failure/invalid-action tests passed'
