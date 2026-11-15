#!/usr/bin/env bash
# 案内のサイトを組み立てる。site/ の中身と、README・docs の画像（docs/images/）、アイコン（web/public/favicon.svg）を
# 出力先のフォルダへコピーする。Pages の workflow と CI が同じこのスクリプトを呼ぶ。
# 使い方: scripts/build-site.sh [出力先]   （既定は _site。出力先は空にしてから作る）
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
out=${1:-_site}
mkdir -p "$out"
out=$(cd "$out" && pwd)
case "$out" in "$root"|"$root/site"|"$root/docs"|"/") echo "出力先が不正: $out" >&2; exit 2;; esac
find "$out" -mindepth 1 -delete
cp -R "$root/site/." "$out/"
mkdir -p "$out/images"
cp -R "$root/docs/images/." "$out/images/"
cp "$root/web/public/favicon.svg" "$out/favicon.svg"
