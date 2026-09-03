#!/usr/bin/env bash
# Install a binary by atomic rename.
#
# Copying over a binary that is currently executing keeps the same inode, and
# macOS then finds a code signature that no longer matches the image it has
# mapped. The running process is killed with OS_REASON_CODESIGNING, and further
# execs of that path can fail until the signature is rewritten.
#
# rename(2) instead swaps the directory entry: the running process keeps the
# old inode and its valid signature, while the next exec picks up the new file.
# The temporary file must live in the destination directory, because rename is
# only atomic within a single filesystem.
#
# Usage: install-binary.sh <source> <destination>
set -euo pipefail

src="${1:?usage: install-binary.sh <source> <destination>}"
dest="${2:?usage: install-binary.sh <source> <destination>}"

[ -f "$src" ] || { echo "install: no such file: $src" >&2; exit 1; }

dest_dir=$(dirname "$dest")
mkdir -p "$dest_dir"

tmp=$(mktemp "$dest_dir/.$(basename "$dest").XXXXXX")
cleanup() { rm -f "$tmp"; }
trap cleanup EXIT

cat "$src" > "$tmp"
chmod 755 "$tmp"

# On macOS an unsigned arm64 binary cannot execute at all. Ad-hoc sign anything
# that arrives unsigned, so a locally built or cross-compiled binary still runs.
if [ "$(uname -s)" = "Darwin" ] && command -v codesign >/dev/null; then
  if ! codesign --verify --strict "$tmp" 2>/dev/null; then
    codesign --sign - --force --timestamp=none "$tmp" >/dev/null 2>&1 || true
  fi
fi

mv -f "$tmp" "$dest"   # atomic within the destination filesystem
trap - EXIT

echo "installed $dest"
"$dest" version 2>/dev/null || true
