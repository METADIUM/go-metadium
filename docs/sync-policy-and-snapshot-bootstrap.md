# go-metadium Sync Policy & Snapshot Bootstrap

**Date:** 2026-06-17
**Author:** Jeffrey Song
**Applies to:** `fix/pre-mainnet-security` / `secfix/mainnet-phase2` and later

---

## 1. Sync policy: full sync only

go-metadium runs **full sync only**. Snap sync is **not supported** — the upstream
snap state-healing path (gentrie redesign, PRs #29313/#30258/#33428) is not
backported to this v1.13.14 base, so the snap path carries known state-integrity
bugs. The default sync mode is therefore `full` (changed in
`eth/ethconfig/config.go`).

For fast new-node bring-up, **do not enable snap** — use the snapshot bootstrap
in §3 instead. It is faster than a full genesis sync and carries no integrity
risk.

### 1.1 How the policy is surfaced to node operators (defense in depth)

1. **Default value** — `ethconfig.Defaults.SyncMode = downloader.FullSync`. A node
   started with no `--syncmode` flag runs full. Operators get the safe behavior
   with zero action. *(done)*
2. **Launcher / unit files** — every shipped config pins `--syncmode full`:
   `metadium/scripts/gmet.sh` (default branch), all `tests/**/docker-compose.yml`,
   and the production systemd units. Keep this explicit so the policy survives a
   default change. *(already true)*
3. **Startup guard (recommended, optional)** — reject or loudly warn when an
   operator passes `--syncmode snap|fast` on the Metadium mainnet/testnet, in
   `cmd/utils/flags.go` near `SetEthConfig`. Hard-reject is strongest; a WARN that
   coerces to full is the minimum. *(not yet implemented — see open item)*
4. **Documentation / release notes** — state the policy in the node-operator
   README and CHANGELOG: "go-metadium supports full sync only; `--syncmode full`
   is the default; for fast bring-up use snapshot bootstrap (this document)."

The combination of (1)+(2)+(4) means a normal operator never has to think about
it; (3) stops a determined operator from silently re-enabling the vulnerable path.

---

## 2. Hard prerequisites for snapshot bootstrap

A snapshot is a file-level copy of one node's chain database into another node.
It only works when **all** of these match between source and target:

- **DB backend** — LevelDB ↔ LevelDB, or RocksDB ↔ RocksDB. Selected at runtime
  by `--userocksdb 0` (LevelDB) / `--userocksdb 1` (RocksDB). A LevelDB snapshot
  **cannot** be loaded by a RocksDB node or vice versa — that needs a full
  re-sync or a DB-conversion tool, not a copy.
  - Source map in this fleet: **46 / mykeepin = LevelDB**, **47 = RocksDB**.
- **Network** — mainnet ↔ mainnet, testnet ↔ testnet (different genesis).
- **Binary** — same/compatible gmet version (fork rules live in the binary, not
  the DB). Use the deployed build.

This procedure is for **sync (follower) nodes**. A governance/sealing node also
needs its own identity and an etcd cluster join — see §4.

---

## 3. Snapshot bootstrap procedure

Layout reference (per node): `<datadir>/geth/chaindata` holds the state DB **and**
the freezer at `<datadir>/geth/chaindata/ancient` (the bulk of the data — it must
be copied). Node identity is `<datadir>/geth/nodekey`; accounts are in
`<datadir>/keystore`. Example datadirs: `/data/gmet-mainnet`, `/data/gmet-testnet`.

### Step 1 — snapshot the SOURCE (consistent copy)

Pick a healthy source with the **same backend + network** as the target.

```bash
# Graceful stop ONLY — never kill -9 (RocksDB flush can take minutes; an
# ungraceful stop leaves the DB needing "Head state missing, repairing").
sudo systemctl stop gmet-<net>        # e.g. gmet-testnet
#   wait until fully stopped:
until [ "$(systemctl is-active gmet-<net>)" = inactive ]; do sleep 3; done

# Archive chaindata (includes the ancient/ freezer subdir).
sudo tar -C /data/gmet-<net>/geth -cf /tmp/chaindata-<net>.tar chaindata

# Bring the source back up immediately.
sudo systemctl start gmet-<net>
```

Notes:
- Downtime = copy time. Pick the least-critical node (e.g. a spare/secondary, or
  47 which is a test node) so production stays up on the others.
- Online copy without stopping is possible (`rsync` twice, second pass after a
  brief stop) but a single live copy of LevelDB/RocksDB files can be inconsistent.
  Stopping the source is the safe default.

### Step 2 — transfer to the TARGET

```bash
scp /tmp/chaindata-<net>.tar <target>:/tmp/      # or rsync
```

### Step 3 — load on the TARGET

```bash
sudo systemctl stop gmet-<net>          # if running; graceful

# Remove any partial chain DB, then extract the snapshot.
sudo rm -rf /data/gmet-<net>/geth/chaindata
sudo tar -C /data/gmet-<net>/geth -xf /tmp/chaindata-<net>.tar

# IMPORTANT: keep the target's OWN nodekey and keystore. We only copied
# chaindata, so nodekey/keystore are untouched — verify they still exist and are
# the target's own (two nodes sharing a nodekey = enode collision).
ls -la /data/gmet-<net>/geth/nodekey

sudo systemctl start gmet-<net>         # --syncmode full, --userocksdb matching backend
```

### Step 4 — verify

```bash
# Head should be ~snapshot height, then climb to tip with no BAD BLOCK.
curl -s -X POST http://127.0.0.1:<rpc> -H 'Content-Type: application/json' \
  --data '{"jsonrpc":"2.0","method":"eth_blockNumber","id":1}'
```

- Head starts near the snapshot height and syncs only the gap → fast.
- Confirm peers > 0 and the block number advances; check the log has no
  `invalid merkle root` / `BAD BLOCK` for newly imported blocks.

---

## 4. Governance / sealing nodes

Snapshot bootstrap gives a node the **chain data** only. A governance (sealing)
node additionally needs:

- its **own** governance-member key / enode registered in the governance node set
  (do **not** reuse another node's nodekey);
- to **join the etcd cluster** (it auto-joins via `etcdAutoJoin` once it is a
  recognized governance member; etcd data is per-node, do **not** copy it).

So: bootstrap chain data with §3, then complete governance membership + etcd join
separately. For pure follower/RPC nodes, §3 alone is sufficient.

---

## Open item

- Startup guard (§1.1.3): add the `--syncmode snap|fast` warn/reject for
  Metadium networks in `cmd/utils/flags.go`. Tracked as an optional phase-2 task.
