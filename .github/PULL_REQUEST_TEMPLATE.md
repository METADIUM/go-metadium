<!-- Base this on `dev`, not `master`. `master` is the release line. -->

## What this changes

<!-- One paragraph: why, not only what. -->

## Compatibility tier

Metadium cannot force exchanges and external node operators to upgrade, so every
change is judged by what a node still running the current release sees. Pick
exactly one tier.

- [ ] **C0 — consensus, loosening.** Accepts a block or transaction the current
      release rejects. **This is a hard fork**: every node that has not upgraded
      splits off. Needs a fork block, release notes, and exchange notice at D-14.
- [ ] **C1 — consensus, tightening.** Rejects something the current release
      accepts. External nodes are unaffected — they accept our blocks either way
      — but a node running the rule stalls on a block that violates it, and the
      only producers are our own BPs. So the question is not how fast we roll,
      it is **whether the blocks being produced today already satisfy the rule**:
      if they do, verifiers roll one at a time like any other release; if they
      do not, **the producers move first** and the verifiers follow. State which
      case this is, with evidence. A rule that also applies to history needs a
      clean sync over the affected range before it is deployed at all.
- [ ] **C2 — P2P wire.** Protocol versions or message shapes. `meta/66` must stay
      advertised; it is the only version shared with the remaining 0.10.x nodes.
- [ ] **C3 — database format.** Breaks the rollback path. Rolling back one minor
      release currently works and must keep working.
- [ ] **C4 — RPC/API.** Does not break a node that stays put, but breaks its
      operator when they upgrade. Removing a namespace or changing a response
      shape needs a deprecation window and notice, whatever upstream did.
- [ ] **C5 — local pool or propagation policy.** No consensus effect; a mixed
      fleet may route transactions differently.
- [ ] **C6 — build or OS baseline.** glibc 2.31 (Ubuntu 20.04) is the floor —
      raising it excludes operators. `make release-check` enforces it.
- [ ] **C7 — internal only.** Tests, docs, tooling, or producer-side behaviour
      that does not change block validity.

**Why this tier:**

<!-- One or two sentences. For C0-C4, state what a node on the current release sees. -->

## Rollout

- [ ] **Testnet BPs first, then verify the rest of the testnet on the old build.**
      Upgrade only the testnet block producers, then confirm the non-BP nodes still
      follow the chain, serve RPC, and accept transactions. **This must pass before
      any mainnet node is touched** — it is the only test that exercises a genuinely
      mixed fleet, which is the state mainnet will be in.
- [ ] Remaining testnet nodes upgraded and soaked
- [ ] Mainnet BPs rolled one at a time (PoA: never stop two at once, never `kill -9`)
- [ ] C1 only: blocks in production already satisfy the rule (evidence above), or
      the producers were upgraded first; and, if the rule applies to history, a
      clean sync over the affected range has passed
- [ ] C0 only: fork block set, release notes published, exchanges notified
- [ ] Not applicable — nothing deploys (C7)

## Verification

<!-- What you ran and what it proved. A test that fails without the change is worth
     more than one that merely covers it. -->

- [ ] Affected packages pass (`make test`, or the specific packages)
- [ ] Both engines build (`CGO_ENABLED=0`, and `-tags rocksdb`)
- [ ] SPoA private network run — if consensus or mining is touched
- [ ] Clean sync over the affected range — if the rule applies to history
