#!/usr/bin/env bash
# Proves snaptwin catches per-instance values that a VM memory snapshot clones.
# Usage: bash demo.sh          (all checks should pass, exit 0)
#        bash demo.sh --break  (restores nothing, boots fresh; checks must fail)
set -u
cd "$(dirname "$0")"

BREAK=0
[ "${1:-}" = "--break" ] && BREAK=1

KERNEL_URL="https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.15/x86_64/vmlinux-6.1.155"
KERNEL_SHA="e20e46d0c36c55c0d1014eb20576171b3f3d922260d9f792017aeff53af3d4f2"
KERNEL=".cache/vmlinux-6.1.155"
OUT=".demo"
BASE_APPEND="console=ttyS0 panic=1 ip=10.0.2.15::10.0.2.2:255.255.255.0::eth0:off"

for tool in go qemu-system-x86_64 curl sha256sum; do
  command -v "$tool" >/dev/null || { echo "missing prerequisite: $tool (see AGENTS.md)"; exit 2; }
done

mkdir -p .cache build "$OUT"
if [ ! -f "$KERNEL" ]; then
  echo "downloading guest kernel (Firecracker CI vmlinux 6.1.155)"
  curl -fsSL -o "$KERNEL.tmp" "$KERNEL_URL" && mv "$KERNEL.tmp" "$KERNEL" || { echo "kernel download failed"; exit 2; }
fi
echo "$KERNEL_SHA  $KERNEL" | sha256sum -c --quiet || { echo "kernel checksum mismatch"; exit 2; }

CGO_ENABLED=0 go build -o build/snaptwin ./cmd/snaptwin || exit 2
CGO_ENABLED=0 go build -o build/guestapp ./examples/guestapp || exit 2
go run ./cmd/mkinitrd build/guestapp build/guestapp.cpio.gz || exit 2

SPEC=examples/guestapp/snaptwin.json
run() {
  local name=$1; shift
  echo
  echo "== snaptwin $name"
  ./build/snaptwin --spec "$SPEC" --kernel "$KERNEL" --initrd build/guestapp.cpio.gz \
    --work "$OUT/$name" --json "$OUT/$name.json" "$@" | tee "$OUT/$name.txt"
  echo "${PIPESTATUS[0]}" > "$OUT/$name.exit"
}

BUGGY_MODE=()
[ $BREAK = 1 ] && BUGGY_MODE=(--cold-boot) && echo "BREAK: the buggy check boots each clone fresh instead of restoring the snapshot"

run buggy "${BUGGY_MODE[@]}" --append "$BASE_APPEND"
run buggy-coldboot --cold-boot --append "$BASE_APPEND"
run fixed --append "$BASE_APPEND snaptwin.mode=fixed"

pass=0; failn=0; failed=()
check() {
  local n=$1 desc=$2; shift 2
  if "$@"; then echo "PASS $n $desc"; pass=$((pass+1)); else echo "FAIL $n $desc"; failn=$((failn+1)); failed+=("$n"); fi
}
exit_is() { [ "$(cat "$OUT/$1.exit")" = "$2" ]; }
has() { grep -Eq "$2" "$OUT/$1.txt"; }
shared_all() { local f=$1; shift; for p in "$@"; do has "$f" "^SHARED +$p " || return 1; done; }

echo
echo "== checks"
check 1 "buggy app restored from one snapshot: snaptwin exits 1" exit_is buggy 1
check 2 "instance-id, session-token and request-id are SHARED across clones" shared_all buggy instance-id session-token request-id
check 3 "buggy app has no /run hook (POST /run -> 404 on every clone)" bash -c "[ \$(grep -c 'POST /run -> 404' $OUT/buggy.txt) -eq 2 ]"
check 4 "crypto/rand read after restore is UNIQUE when the platform reseeds the kernel (vmgenid)" has buggy "^UNIQUE +fresh-random "
check 5 "same buggy app booted fresh twice passes: ordinary tests miss the bug" exit_is buggy-coldboot 0
check 6 "fixed app regenerates state in POST /run: 5/5 probes UNIQUE, exit 0" bash -c "grep -q '^PASS  5/5' $OUT/fixed.txt && [ \$(grep -c 'POST /run -> 200' $OUT/fixed.txt) -eq 2 ] && [ \$(cat $OUT/fixed.exit) = 0 ]"

echo
echo "$pass passed, $failn failed${failed:+ (checks ${failed[*]})}"
[ $failn = 0 ]
