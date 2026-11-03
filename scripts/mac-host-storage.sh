#!/bin/sh
# Run on the macOS host whose data directory is bind-mounted into ARTEX.
set -eu

data_dir=${1:?usage: mac-host-storage.sh DATA_DIR [--watch]}
watch=${2:-}
while :; do
	set -- $(df -Pk "$data_dir" | awk 'NR==2 {print $2, $4}')
	if [ "$#" -ne 2 ]; then exit 1; fi
	tmp=$(mktemp "$data_dir/.host-storage.json.XXXXXX")
	printf '{"total_bytes":%s,"available_bytes":%s,"observed_at":%s,"os":"macOS %s","arch":"%s"}\n' "$(( $1 * 1024 ))" "$(( $2 * 1024 ))" "$(date +%s)" "$(sw_vers -productVersion)" "$(uname -m)" > "$tmp"
	mv "$tmp" "$data_dir/.host-storage.json"
	if [ "$watch" != "--watch" ]; then break; fi
	sleep 60
done
