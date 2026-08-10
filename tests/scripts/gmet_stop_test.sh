#!/bin/bash
# gmet_stop_test.sh - exercise the "stop" paths of metadium/scripts/gmet.sh
#
# Usage:
#   ./gmet_stop_test.sh                  # test metadium/scripts/gmet.sh
#   ./gmet_stop_test.sh /opt/meta/bin/gmet.sh   # test a deployed copy
#
# The fake gmet is a copy of /bin/sleep, so its process name really is "gmet"
# (which is what get_chaindata_holders filters on), and argv[0] is rewritten
# with "exec -a" to control whether get_gmet_pids' command line match hits.

set -u
SRC=${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../metadium/scripts" && pwd)/gmet.sh}
T=$(mktemp -d)
HOLDERS=""
cleanup () {
    for p in $HOLDERS; do kill -9 $p 2>/dev/null; done
    rm -rf "$T"
}
trap cleanup EXIT

mkdir -p "$T/node/bin" "$T/node/geth/chaindata"
cp "$SRC" "$T/node/bin/gmet.sh"
cp /bin/sleep "$T/node/bin/gmet"
echo "fake rocksdb LOG" > "$T/node/geth/chaindata/LOG"
GMETSH="$T/node/bin/gmet.sh"
LOG="$T/node/geth/chaindata/LOG"

PASS=0; FAIL=0
check () { # check <name> <want rc> <got rc> [<must contain>] [<output>]
    if [ "$2" = "$3" ] && { [ $# -lt 4 ] || echo "$5" | grep -q "$4"; }; then
	echo "  PASS  $1 (rc=$3)"; PASS=$((PASS + 1))
    else
	echo "  FAIL  $1 (want rc=$2 got rc=$3, wanted /${4:-}/)"; FAIL=$((FAIL + 1))
	echo "$5" | sed 's/^/        | /'
    fi
}
alive () { kill -0 $1 2>/dev/null; }

# Start a fake node holding chaindata/LOG open, advertising argv[0] "$1".
# Output is detached so it does not hold this shell's pipes open.
start_holder () {
    if [ "${2:-}" = "ignore-term" ]; then
	( exec 9< "$LOG"; trap '' TERM
	  exec -a "$1" "$T/node/bin/gmet" 600 ) > /dev/null 2>&1 &
    else
	( exec 9< "$LOG"; exec -a "$1" "$T/node/bin/gmet" 600 ) > /dev/null 2>&1 &
    fi
    HOLDER=$!
    HOLDERS="$HOLDERS $HOLDER"
    sleep 0.5
}
run_stop () { timeout 60 env "$@" "$GMETSH" stop 2>&1; }

echo "== A. node genuinely stopped: fast path, success =="
OUT=$(run_stop STOP_TIMEOUT=3 LOCK_TIMEOUT=3); RC=$?
check "reports done and exits 0" 0 $RC "done." "$OUT"

echo "== B. node running, command line NOT matched (the fn2 failure) =="
start_holder "$T/node/bin/gmet"   # no "datadir" in argv -> get_gmet_pids misses
OUT=$(run_stop STOP_TIMEOUT=3 LOCK_TIMEOUT=3); RC=$?
check "refuses instead of claiming success" 1 $RC "did not happen" "$OUT"
check "names the offending pid" 1 $RC "$HOLDER" "$OUT"
alive $HOLDER && { echo "  PASS  node left running, not tar-able"; PASS=$((PASS + 1)); } \
    || { echo "  FAIL  node died unexpectedly"; FAIL=$((FAIL + 1)); }
kill -9 $HOLDER 2>/dev/null; sleep 0.3

echo "== C. node running, command line matched: normal SIGTERM path =="
start_holder "$T/node/bin/gmet --datadir $T/node"
OUT=$(run_stop STOP_TIMEOUT=10 LOCK_TIMEOUT=10); RC=$?
check "stops the node and exits 0" 0 $RC "done." "$OUT"
alive $HOLDER && { echo "  FAIL  process survived"; FAIL=$((FAIL + 1)); } \
    || { echo "  PASS  process is gone"; PASS=$((PASS + 1)); }

echo "== D. matched but ignores SIGTERM, STOP_FORCE=0: refuse to SIGKILL =="
start_holder "$T/node/bin/gmet --datadir $T/node" ignore-term
OUT=$(run_stop STOP_TIMEOUT=3 LOCK_TIMEOUT=3 STOP_FORCE=0); RC=$?
check "refuses to SIGKILL" 1 $RC "refusing to SIGKILL" "$OUT"
alive $HOLDER && { echo "  PASS  node left running (clean DB preserved)"; PASS=$((PASS + 1)); } \
    || { echo "  FAIL  node was killed anyway"; FAIL=$((FAIL + 1)); }

echo "== E. same node, STOP_FORCE=1: escalate to SIGKILL =="
OUT=$(run_stop STOP_TIMEOUT=3 LOCK_TIMEOUT=5 STOP_FORCE=1); RC=$?
check "escalates and succeeds" 0 $RC "forcing" "$OUT"
alive $HOLDER && { echo "  FAIL  survived SIGKILL"; FAIL=$((FAIL + 1)); } \
    || { echo "  PASS  process is gone"; PASS=$((PASS + 1)); }

echo "== F. unresolvable data dir: refuse, and do not touch other nodes =="
# A decoy that the degraded 'gmet.*datadir.*' pattern would have matched:
# with an empty dir the old code killed every gmet on the host.
start_holder "$T/node/bin/gmet --datadir /some/other/node"
DECOY=$HOLDER
OUT=$(timeout 60 "$GMETSH" stop "/does/not/exist" 2>&1); RC=$?
check "refuses on unresolvable dir" 1 $RC "cannot resolve" "$OUT"
alive $DECOY && { echo "  PASS  unrelated node untouched"; PASS=$((PASS + 1)); } \
    || { echo "  FAIL  unrelated node was killed"; FAIL=$((FAIL + 1)); }
kill -9 $DECOY 2>/dev/null; sleep 0.3

echo "== G. renamed binary (gmet-rocksdb) holding chaindata: still detected =="
# lsof truncates COMMAND to 9 chars ("gmet-rock"); exact-matching 'gmet'
# could never see this documented deploy practice.
cp /bin/sleep "$T/node/bin/gmet-rocksdb"
( exec 9< "$LOG"; exec -a "gmet-rocksdb" "$T/node/bin/gmet-rocksdb" 600 ) > /dev/null 2>&1 &
HOLDER=$!; HOLDERS="$HOLDERS $HOLDER"; sleep 0.5
OUT=$(run_stop STOP_TIMEOUT=3 LOCK_TIMEOUT=3); RC=$?
check "refuses instead of claiming success" 1 $RC "did not happen" "$OUT"
check "names the renamed holder pid" 1 $RC "$HOLDER" "$OUT"
kill -9 $HOLDER 2>/dev/null; sleep 0.3

echo
echo "PASS=$PASS FAIL=$FAIL"
[ $FAIL -eq 0 ]
