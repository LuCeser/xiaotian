#!/usr/bin/env bash
# stress-bounded.sh —— 有界的负载复现（替代手工 busy loop）。
# 限制：核数上限 min(-n, nproc-2, 8)，时长上限 600s，全部 nice + timeout，退出自动清理。
# 用法：scripts/harness/stress-bounded.sh -n 4 -t 120 -- go test -race -count=3 ./pkg/session/ -run TestLoad
set -euo pipefail

N=4; T=120
while [ $# -gt 0 ]; do
  case "$1" in
    -n) N="$2"; shift 2 ;;
    -t) T="$2"; shift 2 ;;
    --) shift; break ;;
    *) echo "usage: $0 -n <cpus> -t <seconds> -- <command...>" >&2; exit 2 ;;
  esac
done
[ $# -gt 0 ] || { echo "usage: $0 -n <cpus> -t <seconds> -- <command...>" >&2; exit 2; }

NPROC="$(nproc 2>/dev/null || echo 4)"
[ "$N" -gt $(( NPROC - 2 )) ] && N=$(( NPROC - 2 ))
[ "$N" -lt 1 ] && N=1
[ "$N" -gt 8 ] && N=8
[ "$T" -gt 600 ] && T=600
[ "$T" -lt 1 ] && T=1

PIDS=()
cleanup() {
  for p in "${PIDS[@]:-}"; do kill "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

echo "stress-bounded: ${N} 个负载核，${T}s 上限（nice+timeout，共享机器友好）"
for i in $(seq 1 "$N"); do
  nice -n 19 timeout "$T" bash -c 'while :; do :; done' >/dev/null 2>&1 &
  PIDS+=($!)
done
sleep 1
uptime
rc=0
"$@" || rc=$?
echo "stress-bounded: 命令退出码 $rc（负载进程由 trap 清理）"
exit "$rc"
