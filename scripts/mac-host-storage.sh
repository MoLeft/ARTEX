#!/bin/sh
# Run on the macOS host whose data directory is bind-mounted into ARTEX.
set -eu

data_dir=${1:?usage: mac-host-storage.sh DATA_DIR [--watch] [CONTAINER] [DOCKER_BIN]}
watch=${2:-}
container=${3:-}
docker_bin=${4:-docker}
while :; do
	set -- $(df -Pk "$data_dir" | awk 'NR==2 {print $2, $4}')
	if [ "$#" -ne 2 ]; then exit 1; fi
	snapshot=$(printf '{"total_bytes":%s,"available_bytes":%s,"observed_at":%s,"os":"macOS %s","arch":"%s"}\n' "$(( $1 * 1024 ))" "$(( $2 * 1024 ))" "$(date +%s)" "$(sw_vers -productVersion)" "$(uname -m)")
	if [ -n "$container" ]; then
		printf '%s\n' "$snapshot" | "$docker_bin" exec -i "$container" sh -c 'set -eu; tmp=$(mktemp /app/data/.host-storage.json.XXXXXX); trap '\''rm -f "$tmp"'\'' EXIT; cat > "$tmp"; mv "$tmp" /app/data/.host-storage.json'
	else
		tmp=$(mktemp "$data_dir/.host-storage.json.XXXXXX")
		printf '%s\n' "$snapshot" > "$tmp"
		mv "$tmp" "$data_dir/.host-storage.json"
	fi
	if [ "$watch" != "--watch" ]; then break; fi
	sleep 60
done
