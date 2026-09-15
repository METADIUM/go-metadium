# Release Build Toolchain — Standing Rules and Known Limits

Status: reference, 2026-08-13; versions and in-tree anchors re-checked
2026-09-09. Companion to the README's "Release artifacts" section, which covers
how to build them. This covers what follows from how they are built. Every
version below was read out of the image or the archive rather than taken from
memory; the commands are included so the next reader can re-check instead of
trusting the date on this file.

## 1. Static libstdc++ means security fixes arrive by rebuild, not by upgrade

Release artifacts are linked with `STATIC_STDCPP=YES`, which resolves to
`-l:libstdc++.a -static-libgcc` (`Makefile`, `STDCPP_LDFLAGS`). That is what lets
a binary built on Ubuntu 20.04 run on every newer release: it carries no
`GLIBCXX` or `CXXABI` version requirement at all, which `make release-check`
enforces.

The consequence is that the distribution's update channel no longer reaches that
code. A libstdc++ or libgcc fix on a node does nothing for `gmet`, because the
copy `gmet` uses is inside the binary.

**The rule: a toolchain CVE is a rebuild and a re-release, not a distro
package update.** Nothing in the fleet will report the old code as present, and
no node-side upgrade will remove it.

What is *not* static, and therefore is still the distribution's job on each
node: glibc, and for RocksDB artifacts snappy, lz4, zstd, jemalloc and libudev.
`make release-check` prints each artifact's `NEEDED` list for exactly this
reason — those libraries must exist on the target host and their updates do
apply normally.

## 2. The image is one dependency bump away from a confusing failure

The builder image is Ubuntu 20.04 (focal), whose zstd is **1.4.4**:

```console
$ docker run --rm ubuntu:focal bash -c 'apt-get update -qq; apt-cache policy libzstd-dev'
  Candidate: 1.4.4+dfsg-3ubuntu0.1
```

The vendored RocksDB (submodule `5fbc1cd5bcf63782675168b98e114151490de6d9`) gates
part of its dictionary-compression support on a newer zstd —
`util/compression.h`:

```c
// ZDICT_finalizeDictionary API is exported and stable since v1.4.5
#if ZSTD_VERSION_NUMBER >= 10405
```

So on focal that code compiles out. Today that costs nothing, because this node
never selects a RocksDB compression type: the options it builds set only
`create_if_missing` and `max_open_files` (`ethdb/rocksdb/rocksdb.go`), leaving
RocksDB's own default, and dictionary compression is not in play.

What to expect if that changes: **a RocksDB bump that raises the zstd floor to
1.4.5 breaks the release image, and the error will look like a compiler
problem** — a missing symbol or a failed feature test, in C++ output, several
hundred lines into a RocksDB build. It is a base-image problem. Check the
distribution's zstd before reading the compiler output.

## 3. The base is past standard support, and jammy is the exit

focal left standard support on **31 May 2025**; Expanded Security Maintenance
began 29 May 2025 and runs to May 2030. ESM requires an attached Ubuntu Pro
subscription, and the public `ubuntu:focal` image this build pulls does not have
one — so the `apt-get update && apt-get upgrade` in the first layer no longer
picks up new security fixes for most packages. It is still the right base today
for one reason only: it is the oldest distribution in the fleet, and the
artifacts have to run there.

Ubuntu 22.04 (jammy) removes two of the three awkward things about the current
image at once:

```console
$ docker run --rm ubuntu:jammy bash -c 'apt-get update -qq; apt-cache policy gcc libc6 libzstd-dev'
gcc         4:11.2.0-1ubuntu1
libc6       2.35-0ubuntu3.15
libzstd-dev 1.4.8+dfsg-3build1
```

(The `libc6` revision moves with the archive — it read `3.14` in August and
`3.15` in September. What matters is the `2.35` in front of it.)

- gcc 11 is in the archive, so `ppa:ubuntu-toolchain-r/test` — a community PPA
  that keeps only its newest build, and a single point of failure for the release
  build — is no longer needed.
- zstd is 1.4.8, above the floor in §2.

The blocker is the fleet, not the build: moving the base raises the glibc floor
from 2.31 to 2.35, and any node still on 20.04 could no longer run the
artifacts. That is why this waits on the remaining 20.04 nodes being upgraded or
replaced.

When it happens, the number to change is `MAX_GLIBC` in the `Makefile`. It is the
one place the ceiling is written down, and `release-check` fails a build that
exceeds it — so the sequence is: fleet off 20.04, then base image, then
`MAX_GLIBC`, in that order. Raising `MAX_GLIBC` first would remove the check that
protects the nodes still running.
