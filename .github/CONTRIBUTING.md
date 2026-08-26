# Contributing

This repository is Metadium's node implementation, forked from
[go-ethereum](https://github.com/ethereum/go-ethereum). Contributions are
welcome — fork, fix, commit, and open a pull request.

If a change is larger than a fix, open an issue first and describe the approach.
That is cheaper than discovering in review that the change cannot ship for a
reason that has nothing to do with its code.

## Pull requests

 * **Open pull requests against `dev`, not `master`.** `master` is the release
   line: it carries tagged releases and is advanced from `dev` per release.
 * Fill in the pull request template. Two parts of it are not optional:
   * **Compatibility tier.** Metadium cannot require exchanges and external node
     operators to upgrade, so every change is judged by what a node still running
     the current release sees. A change that makes us accept something older
     nodes reject is a hard fork, and calling it anything else is how a network
     splits by accident.
   * **Rollout.** Testnet block producers are upgraded first, and the remaining
     testnet nodes are then verified on the old build, before any mainnet node is
     touched. That is the only test that exercises a mixed fleet, which is the
     state mainnet passes through and external operators stay in.
 * Say what your tests prove. A test that fails without the change is worth more
   than one that merely covers it.

## Coding guidelines

Please make sure your contributions adhere to our coding guidelines:

 * Code must adhere to the official Go
[formatting](https://golang.org/doc/effective_go.html#formatting) guidelines
(i.e. uses [gofmt](https://golang.org/cmd/gofmt/)).
 * Code must be documented adhering to the official Go
[commentary](https://golang.org/doc/effective_go.html#commentary) guidelines.
 * Commit messages should be prefixed with the package(s) they modify.
   * E.g. "eth, rpc: make trace configs optional"

## Building and testing

See the README for build commands, the release build, and the local three-node
PoA network. Both database engines have to build: the default LevelDB build and
`-tags rocksdb`.

## Configuration, dependencies, and tests

Much of the tree is still upstream go-ethereum, so the
[Developers' Guide](https://geth.ethereum.org/docs/developers/geth-developer/dev-guide)
remains a good reference for environment setup, dependency management, and
testing procedures.
