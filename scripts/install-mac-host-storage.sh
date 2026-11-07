#!/bin/sh
set -eu

data_dir=$(cd "${1:?usage: install-mac-host-storage.sh DATA_DIR [CONTAINER]}" && pwd)
container=${2:-}
docker_bin=
if [ -n "$container" ]; then docker_bin=$(command -v docker); fi
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
script="$script_dir/mac-host-storage.sh"
plist="$HOME/Library/LaunchAgents/com.moleft.artex.host-storage.plist"
test -f "$script"

python3 - "$script" "$data_dir" "$plist" "$container" "$docker_bin" <<'PY'
import os
import plistlib
import sys

script, data_dir, path, container, docker_bin = sys.argv[1:]
os.makedirs(os.path.dirname(path), exist_ok=True)
with open(path, "wb") as output:
    plistlib.dump({
        "Label": "com.moleft.artex.host-storage",
        "ProgramArguments": ["/bin/sh", script, data_dir, "--watch", container, docker_bin],
        "RunAtLoad": True,
        "KeepAlive": True,
        "ThrottleInterval": 60,
    }, output)
PY

domain="gui/$(id -u)"
launchctl bootout "$domain" "$plist" 2>/dev/null || true
launchctl bootstrap "$domain" "$plist"
