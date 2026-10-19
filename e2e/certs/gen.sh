#!/usr/bin/env bash
# Generates a throwaway CA and a leaf certificate for the proem services in the
# e2e compose network. Run once per e2e run; the output is never committed.
set -euo pipefail

OUT="$(cd "$(dirname "$0")" && pwd)/out"
rm -rf "$OUT"
mkdir -p "$OUT"
cd "$OUT"

openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj "/CN=proem e2e CA" \
  -keyout ca.key -out ca.pem 2>/dev/null

openssl req -newkey rsa:2048 -nodes -subj "/CN=proem" \
  -keyout proem.key -out proem.csr 2>/dev/null
cat > leaf.ext <<'EXT'
basicConstraints=CA:FALSE
keyUsage=digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=DNS:proem,DNS:proem-short,DNS:mock-upstream,DNS:localhost,IP:127.0.0.1
EXT
openssl x509 -req -in proem.csr -CA ca.pem -CAkey ca.key -CAcreateserial \
  -days 2 -extfile leaf.ext -out proem.pem 2>/dev/null

rm -f proem.csr leaf.ext ca.srl ca.key
# The proem container runs as nonroot and must read its key. Test-only key.
chmod 0644 ./*
echo "certificates written to $OUT"
