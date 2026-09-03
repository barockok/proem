#!/usr/bin/env bash
# Verifies that install-binary.sh replaces a binary by rename rather than by
# writing through the existing inode. Overwriting in place leaves macOS with a
# stale signature for that inode, after which the path can no longer execute.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
install_sh="$here/../install-binary.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

mkdir -p "$tmp/bin"
printf '#!/bin/sh\necho v1\n' > "$tmp/v1"; chmod +x "$tmp/v1"
printf '#!/bin/sh\necho v2\n' > "$tmp/v2"; chmod +x "$tmp/v2"

"$install_sh" "$tmp/v1" "$tmp/bin/prog" >/dev/null
before=$(ls -i "$tmp/bin/prog" | awk '{print $1}')

"$install_sh" "$tmp/v2" "$tmp/bin/prog" >/dev/null
after=$(ls -i "$tmp/bin/prog" | awk '{print $1}')

[ "$("$tmp/bin/prog")" = "v2" ] || { echo "FAIL: content not replaced"; exit 1; }
[ "$before" != "$after" ] || { echo "FAIL: inode $before reused, so the file was overwritten in place"; exit 1; }
[ -x "$tmp/bin/prog" ] || { echo "FAIL: not executable"; exit 1; }

# no temporary files left behind
leftovers=$(find "$tmp/bin" -name '.prog.*' | wc -l | tr -d ' ')
[ "$leftovers" = "0" ] || { echo "FAIL: $leftovers temp files left"; exit 1; }

# a missing source is an error, and must not destroy the installed binary
if "$install_sh" "$tmp/nope" "$tmp/bin/prog" >/dev/null 2>&1; then
  echo "FAIL: missing source should exit non-zero"; exit 1
fi
[ "$("$tmp/bin/prog")" = "v2" ] || { echo "FAIL: failed install damaged the target"; exit 1; }

echo "install-binary.sh: OK (inode $before -> $after)"
