#!/usr/bin/env python3
"""measure.py - block production performance on the running private network
(§11.3), PBFT or, with bftBlock far ahead, the PoA baseline.

  M-01  confirmation latency: value transfers sent one at a time from node1,
        each timed from eth_sendTransaction to its receipt (p50, p99)
  M-02  idle: empty-block intervals seen at node1 and round changes while idle
  M-03  under load: round changes and committed transfers per second while
        several senders keep the pool busy
  M-05, M-07, M-09, M-10 (with --metrics): the node timers over the run,
        read from each validator's /debug/metrics; the nodes must run with
        NODE_ARGS="--metrics --metrics.addr 127.0.0.1" (port 6060)

Usage: ./measure.py [--latency N] [--idle BLOCKS] [--load SECONDS] [--metrics]

Resolution: M-01 polls for the receipt every 5 ms and M-02 polls the head
every 20 ms, so each carries up to that much quantisation; differences of
that size are not real. M-03's transfers per second is bounded by the
senders here, synchronous RPC from one host that may also run the
validators: read it as "everything sent was committed, with no round
changes", not as the chain's throughput (M-06 is).
"""
import argparse, json, statistics, subprocess, threading, time, urllib.request

NODE1 = "http://localhost:8645"


def rpc(method, params=None, url=NODE1):
    body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params or []}).encode()
    req = urllib.request.Request(url, body, {"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=10) as r:
        d = json.load(r)
    if "error" in d:
        raise RuntimeError(d["error"])
    return d["result"]


def head():
    return int(rpc("eth_blockNumber"), 16)


def block(n):
    return rpc("eth_getBlockByNumber", [hex(n), False])


def accounts():
    with open("node-ids.txt") as f:
        return [line.split()[2] for line in f]


def pct(xs, p):
    xs = sorted(xs)
    return xs[min(len(xs) - 1, int(round(p / 100 * (len(xs) - 1))))]


def latency(n):
    acc = accounts()
    out = []
    for i in range(n):
        t0 = time.perf_counter()
        h = rpc("eth_sendTransaction", [{"from": acc[0], "to": acc[1], "value": "0x1"}])
        while rpc("eth_getTransactionReceipt", [h]) is None:
            time.sleep(0.005)
        out.append((time.perf_counter() - t0) * 1000)
    return out


def idle(blocks):
    """Arrival times of new heads at node1, polled every 20 ms."""
    seen, last = [], head()
    while len(seen) < blocks + 1:
        h = head()
        if h > last:
            seen.append((time.perf_counter(), h))
            last = h
        time.sleep(0.02)
    gaps = [b[0] - a[0] for a, b in zip(seen, seen[1:])]
    first, last = seen[0][1], seen[-1][1]
    rounds = [int(block(n).get("bftRound", "0x0"), 16) for n in range(first + 1, last + 1)]
    return gaps, rounds


def load(seconds, senders=4):
    acc = accounts()
    stop = time.time() + seconds
    sent = [0] * senders

    def run(i):
        while time.time() < stop:
            try:
                rpc("eth_sendTransaction", [{"from": acc[0], "to": acc[(i % (len(acc) - 1)) + 1], "value": "0x1"}])
                sent[i] += 1
            except Exception:
                time.sleep(0.05)

    start = head()
    ts = [threading.Thread(target=run, args=(i,)) for i in range(senders)]
    for t in ts:
        t.start()
    for t in ts:
        t.join()
    time.sleep(3)  # let the last blocks commit
    end = head()
    txs, rounds, times = 0, [], []
    for n in range(start + 1, end + 1):
        b = block(n)
        txs += len(b["transactions"])
        rounds.append(int(b.get("bftRound", "0x0"), 16))
        times.append(int(b["timestamp"], 16))
    span = max(1, times[-1] - times[0]) if times else 1
    # sent counts transfers the node accepted; more committed than that
    # means something else is sending, and the numbers are not these.
    assert txs <= sum(sent), f"{txs} committed but only {sum(sent)} sent"
    return sum(sent), txs, rounds, span, end - start


# name in /debug/metrics -> what it times
TIMERS = [
    ("metabft/wal/sync", "M-05 WAL fsync, per record"),
    ("metabft/proposal/verify", "M-07 proposal check, whole"),
    ("metabft/proposal/execute", "M-07 proposal check, execution"),
    ("chain/execution", "M-07 import, execution"),
    ("chain/validation", "M-07 import, state validation"),
    ("miner/bft/floorcheck", "M-09 floor check, per transaction"),
    ("metabft/proposal/sidecars", "M-10 sidecar fetch before PREPARE"),
]


def block_creation_time():
    """Governance's blockCreationTime, as node1's metadium_getBlockBuildParameters reports it."""
    try:
        return int(rpc("admin_metadiumInfo")["blockInterval"])
    except Exception:
        return "?"


def node_metrics(n):
    out = subprocess.run(["docker", "exec", f"gmet-pbft-node{n}", "curl", "-s", "localhost:6060/debug/metrics"],
                         capture_output=True, text=True, timeout=20)
    return json.loads(out.stdout)


def metrics_report(n, blocks):
    """Per timer: count summed over the validators, and the median over them
    of each one's mean and p99 (timers are in ns; reservoir percentiles)."""
    per = [node_metrics(i) for i in range(1, n + 1)]
    print(f"timers over the run, {n} validators ({blocks} blocks at node1):")
    for name, what in TIMERS:
        rows = [m for m in per if m.get(name + ".count", 0) > 0]
        if not rows:
            print(f"  {what:38s} no samples")
            continue
        count = sum(m[name + ".count"] for m in rows)
        mean = statistics.median(m[name + ".mean"] for m in rows) / 1e6
        p99 = statistics.median(m[name + ".99-percentile"] for m in rows) / 1e6
        print(f"  {what:38s} {count:8d} samples, mean {mean:7.2f} ms, p99 {p99:7.2f} ms")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--latency", type=int, default=100)
    ap.add_argument("--idle", type=int, default=12)
    ap.add_argument("--load", type=int, default=60)
    ap.add_argument("--metrics", action="store_true", help="report the node timers (needs --metrics on the nodes)")
    a = ap.parse_args()
    n = len(accounts())
    print(f"=== measurements, N={n} ===")
    first = head()

    lat = latency(a.latency)
    print(f"M-01 latency over {len(lat)} transfers: p50 {pct(lat, 50):.0f} ms, p99 {pct(lat, 99):.0f} ms, "
          f"min {min(lat):.0f}, max {max(lat):.0f}")

    gaps, rounds = idle(a.idle)
    print(f"M-02 idle: {len(gaps)} intervals, mean {statistics.mean(gaps):.2f} s, "
          f"min {min(gaps):.2f}, max {max(gaps):.2f}; round changes {sum(1 for r in rounds if r > 0)}")

    sent, txs, rounds, span, blocks = load(a.load)
    print(f"M-03 load {a.load} s: {sent} sent, {txs} committed in {blocks} blocks "
          f"({txs / span:.0f} tx/s by block time); blocks above round 0: {sum(1 for r in rounds if r > 0)}")
    print(f"M-04 block interval under load: {span / max(1, blocks - 1):.2f} s mean by block time "
          f"(governance blockCreationTime {block_creation_time()} ms)")

    if a.metrics:
        metrics_report(n, head() - first)


if __name__ == "__main__":
    main()
