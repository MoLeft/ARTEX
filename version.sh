#!/usr/bin/env bash
# Version each first-parent main commit after the last legacy beta tag.
set -euo pipefail

cd "$(dirname "$0")"
ref="${1:-HEAD}"
base_tag="v0.26.11.01.002-beta1"
git merge-base --is-ancestor "$base_tag" "$ref" || {
  echo "版本计算仅支持 $base_tag 之后的提交" >&2
  exit 1
}

day="26.11.01"
sequence=2
while IFS= read -r commit; do
  commit_day="$(TZ=Asia/Shanghai git show -s --format=%cd --date=format-local:%y.%m.%d "$commit")"
  if [[ "$commit_day" == "$day" ]]; then
    sequence=$((sequence + 1))
  else
    day="$commit_day"
    sequence=1
  fi
done < <(git rev-list --first-parent --reverse "$base_tag..$ref")

if ((sequence > 999)); then
  echo "当日提交序号超过 999" >&2
  exit 1
fi
printf 'v0.%s.%03d\n' "$day" "$sequence"
