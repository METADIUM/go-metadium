# Review rules

Rules for automated and human review of pull requests in this repository. The
pull request template already asks the author to declare a compatibility tier
and a rollout plan; this file says what a reviewer should independently check
and what is worth raising.

## What this repository is

A fork of go-ethereum v1.13.14 running Metadium PoA. Two facts drive almost
every rule below:

1. **We cannot force external node operators to upgrade.** A mixed fleet —
   current release alongside older releases — is the normal state, not an edge
   case. Every change is judged by what a node still running the current
   release sees.
2. **Block production is PoA and the producers are few.** A rule that stalls a
   verifier is recoverable; a change that stops producers is not.

Pull requests target `dev`. `master` is the release line.

## 1. Verify the declared compatibility tier

This is the highest-value thing a review can do here. The author picks one of
C0–C7 in the template. **Derive the tier independently from the diff and flag
any mismatch**, with the reason.

Mis-grades seen most often:

- **Loosening disguised as a fix.** Any change that makes previously-rejected
  input acceptable is C0 — a hard fork — however small it looks. "It only
  accepts a value that should always have been valid" is still C0.
- **Validation changes graded C7** because they read like a defensive check.
  If it can change whether a block or transaction is accepted, it is C0 or C1.
- **Edits under `params/`** — fork blocks, gas constants, chain config — are
  never C7.
- **RPC removals and response-shape changes** are C4 even when upstream made
  the same change. Following upstream is not a justification on its own.
- **Toolchain, cgo flags, or linked library version bumps** are C6.
- **Wire protocol.** `meta/66` must stay advertised; it is the only version
  shared with the remaining older nodes.
- **A single branch can span tiers.** Grade the strictest part and say which
  hunk drives it, rather than averaging.

For C1, the template asks whether blocks in production already satisfy the new
rule. Flag a C1 that asserts this without evidence — the answer decides whether
verifiers or producers move first.

## 2. Consensus, sealing, and rewards

- Changes to sealing or reward distribution need evidence from a multi-node
  run. A passing unit test is not sufficient.
- **Never accept a blanket `rewardPool == nil` shortcut.** A nil-pool path must
  be scoped to the chain it is meant for; an unscoped one breaks mainnet. If a
  PR adds one, require the chain-scoped gate and a test that would fail without
  it.
- Consensus-affecting changes should show a fork-crossing or boundary run, not
  only steady-state blocks.
- Flag any script, unit file, or document that implies stopping more than one
  producer at a time.

## 3. Shutdown and process lifecycle

- **`kill -9`, `SIGKILL`, and automatic escalation to them must not appear** in
  anything that can touch a running node — scripts, systemd units, runbooks,
  examples. RocksDB in particular does not survive it cleanly. This is
  blocking.
- A force-stop default that escalates after a timeout is a defect, not a
  convenience. Flag newly introduced ones.
- Long shutdowns during initial sync are a known behaviour. Flag any change
  that "fixes" them by shortening the stop timeout.
- Log rotation and similar side-channels have killed the process before. Flag
  changes that can terminate or restart the node as a side effect of
  housekeeping.

## 4. Database engine

- RocksDB is selected by build tag. **A binary built without `-tags rocksdb`
  that is then told to use RocksDB gets LevelDB instead, silently** — the
  no-tag build of `ethdb/rocksdb` forwards `New` straight to LevelDB, and
  nothing reports the substitution. Flag any engine selection path that does
  not verify the running build actually supports the requested engine, and any
  change that makes the fallback quieter.
- Engine choice is not the flag alone: `rawdb.NewDB` detects the engine of an
  existing `chaindata` first and only falls back to `params.UseRocksDb` (which
  defaults to RocksDB) for a fresh directory. Flag changes that let the flag
  override a detected engine — that is how a datadir gets opened by the wrong
  engine.
- `chaindata` cannot be moved between engines. Flag documentation, scripts, or
  comments that imply migration or cross-engine recovery is possible.
- Do not raise the on-disk format (C3). Rolling back one minor release
  currently works and must keep working.

## 5. Networking and peers

- `static-nodes.json` is no longer honoured — the node logs an error and
  ignores it. Static peers belong in `config.toml` (`P2P.StaticNodes`). Flag
  anything that *relies* on the file taking effect: docs or runbooks telling an
  operator to create one, or a change that places one in a datadir and expects
  peering to follow. Mentioning the filename is not by itself a defect; the
  private-network harness keeps one as a local source for the bootnode enode
  and wires the peer another way.
- Nodes behind NAT cannot rely on discovery. Flag changes that assume discovery
  is always available, or that remove a static-peer path as redundant.
- **Re-dial and reconnect logic:** a dialer that stops retrying and never
  recovers without a restart is a known failure class in this codebase and
  upstream. Flag changes to dial scheduling, backoff, or peer-slot accounting
  that do not come with a test for the stalled case.
- Snap sync is not usable on this network and `gmet` refuses `--syncmode snap`;
  full sync is the only mode. Flag documentation or tooling that presents snap
  as an available option.
- **But the snap protocol is still advertised and its handlers are reachable.**
  Capability negotiation keys off `SnapshotCache` (default 102), not the sync
  mode, so a node running full sync still offers `snap/1` and will be sent snap
  messages. Do not treat `eth/protocols/snap` as dead code or dismiss a defect
  there as unreachable — an unsolicited snap response is exactly a path an
  arbitrary peer can drive.

## 6. Upstream backports

- The base is far behind upstream. **A cherry-pick that compiles is not
  evidence that it is correct on this base.** Ask what the patch depended on
  upstream and whether that dependency exists here.
- Flag backports that reach into a different generation of the transaction pool
  or state management than this base uses.
- Transaction size policy is deliberately capped, at
  `params.MaxTransactionSize` (262144). Flag upstream changes that conflict
  with it, including intrinsic-gas and calldata-cost changes that assume a
  different limit.
- Security backports should state the CVE and whether this fork was actually
  affected. "Upstream patched it" is not the same as "we were vulnerable".

## 7. Build and release

- Local builds link against the host and are not portable. Flag any instruction
  or automation that publishes or deploys a locally-built binary; released
  artifacts go through the release container and `make release-check`.
- The glibc floor is 2.31. Raising it excludes operators; `make release-check`
  enforces it, so also flag changes that weaken that check.
- Both engines must build: `CGO_ENABLED=0`, and `-tags rocksdb`.
- Release notes: the published release body is the source of truth. Flag
  tooling that replaces the body wholesale from a local file — it silently
  drops edits made after publication.

## 8. RPC and read-path derivation

Consensus is well covered here; the read path is where a defect has survived
longest. Receipt fields that are derived at read time went null for months
because the assignment was dropped in a rebase and nothing compared the RPC
output to anything.

- A change to the header or receipt shape must run the RPC-facing packages, not
  only the consensus ones. `ethclient` compares a header fetched over RPC
  against an in-memory one field by field, so a new or newly-populated field
  surfaces there first.
- **Golden files are not verification.** Regenerating them makes them agree
  with whatever the code now does, including a regression. Flag a PR that
  updates golden output without saying what changed and why the new value is
  right.
- Prefer a differential check against a known-good source (an older release, or
  a second method that derives the same value) over an assertion written from
  the implementation.

## 9. Tests

- Prefer a test that fails without the change over one that merely covers it.
  Say so when a test would pass either way.
- **Do not assert on the balance of an account that also seals.** Sealing
  income lands in the same window, so a "balance decreased" assertion on a
  producer account fails against a correct node on any network that pays
  rewards. Assert the property under test directly instead.
- Consensus or mining touched → private-network evidence expected.
- A rule that applies to history → a clean sync over the affected range is
  expected before deployment, and the PR should say whether it ran.
- Flag new test helpers that depend on a specific local environment rather than
  the provided harness.

## 10. Hygiene — blocking

- **No assistant or AI attribution anywhere.** `Co-Authored-By` trailers,
  "generated with" footers, or similar markers in commit messages, PR
  descriptions, or file headers must be removed before merge.
- **No operational detail.** Node addresses, hostnames, account names, service
  names, key material, or internal URLs must not appear in code, tests,
  fixtures, or documentation. This repository is public. Operational notes
  belong in untracked local files.
- Pull requests must be based on `dev`, not `master`.
- **Stacked branches: read the three-dot diff.** When a branch is based on a
  commit that is not yet in `dev`, a two-dot `dev branch` diff shows the
  intervening work as deletions that are not really there. Use `dev...branch`
  before calling anything a removal.

## What not to raise

Reviews are billed per run and land on a public repository, so keep the signal
high:

- Unmodified upstream go-ethereum code that merely appears in the diff context.
- Style preferences that the surrounding file already settled.
- Missing tests for documentation or comment-only changes.
- Re-arguing the branch strategy or the tier taxonomy itself.
- Restating a template checklist item the author already answered truthfully.

## Severity

**Blocking** — wrong tier on a consensus-affecting change; an unscoped nil
reward path; `kill -9` or force-stop defaults; silent engine fallback;
operational detail or key material; assistant attribution; a C0 without a fork
block.

**Non-blocking** — naming, comment wording, test coverage of paths that cannot
affect block validity, and anything the author explicitly deferred to a
follow-up issue with a reference.
