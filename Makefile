# This Makefile is meant to be used by people that do not usually work
# with Go source code. If you know what GOPATH is then you probably
# don't need to bother with make.

.PHONY: geth evm all test test-short lint fmt clean devtools help rocksdb
.PHONY: gmet gmet-linux metadium logrot dbbench release-check

GOBIN = ./build/bin
GO ?= latest
GORUN = go run

# USE_ROCKSDB
# - undefined | "NO": Do not use
# - "YES": build a static lib from rocksdb directory, and use that one
# - "EXISTING": use existing rocksdb shared lib.
ifndef USE_ROCKSDB
  ifeq ($(shell uname), Linux)
    USE_ROCKSDB = YES
  else
    USE_ROCKSDB = NO
  endif
endif
ifneq ($(shell uname), Linux)
  USE_ROCKSDB = NO
endif

# STATIC_STDCPP
# - "YES": link libstdc++ from its archive and libgcc statically, so the binary
#   carries no GLIBCXX/CXXABI requirement and does not inherit the build host's
#   floor. Release artifacts need this; gmet-linux sets it. Requires the static
#   libstdc++ (libstdc++-N-dev on Debian/Ubuntu, libstdc++-static on RHEL).
# - undefined | anything else: link the shared libstdc++, which is what a plain
#   development box has. -static-libgcc is paired with the static libstdc++ so
#   the unwinder matches; do not drop one without the other.
ifeq ($(STATIC_STDCPP), YES)
STDCPP_LDFLAGS = -l:libstdc++.a -static-libgcc
else
STDCPP_LDFLAGS = -lstdc++
endif

# gmet-linux always compiles inside a Linux container, so the host's uname must
# not pick the engine for it. Default to RocksDB there and honour USE_ROCKSDB
# only when a person set it — on the command line or in the environment. The
# assignment above is uname-driven and must not leak into the container build,
# which is why the file origin is excluded. The environment is accepted because
# this repository configures builds that way elsewhere (dev-ci.yml passes
# CFLAGS/CXXFLAGS through the environment), and silently building the other
# engine is worse than either honouring or rejecting the variable.
USE_ROCKSDB_ORIGIN := $(origin USE_ROCKSDB)
ifeq ($(USE_ROCKSDB_ORIGIN),command line)
GMET_LINUX_USE_ROCKSDB = $(USE_ROCKSDB)
else ifeq ($(USE_ROCKSDB_ORIGIN),environment)
GMET_LINUX_USE_ROCKSDB = $(USE_ROCKSDB)
else ifeq ($(USE_ROCKSDB_ORIGIN),environment override)
GMET_LINUX_USE_ROCKSDB = $(USE_ROCKSDB)
else
GMET_LINUX_USE_ROCKSDB = YES
endif

# Highest glibc symbol version release artifacts may reference. Set by the
# oldest distribution in the deployment fleet (Ubuntu 20.04 = 2.31).
MAX_GLIBC ?= 2.31

ifneq ($(USE_ROCKSDB), NO)
ROCKSDB_DIR=$(shell pwd)/rocksdb
ROCKSDB_TAG=-tags rocksdb
# Recursively expanded on purpose: make_config.mk only exists once the rocksdb
# target has run.
ROCKSDB_CGO_CFLAGS = -I$(ROCKSDB_DIR)/include
ROCKSDB_CGO_LDFLAGS = -L$(ROCKSDB_DIR) -lrocksdb -lm $(STDCPP_LDFLAGS) $(shell awk '/PLATFORM_LDFLAGS/ {sub("PLATFORM_LDFLAGS=", ""); print} /JEMALLOC=1/ {print "-ljemalloc"}' < $(ROCKSDB_DIR)/make_config.mk)
endif

metadium: gmet logrot
	@[ -d build/conf ] || mkdir -p build/conf
	@cp -p metadium/scripts/gmet.sh metadium/scripts/solc.sh build/bin/
	@cp -p metadium/scripts/config.json.example		\
		metadium/scripts/genesis-template.json		\
		metadium/contracts/MetadiumGovernance.js	\
		metadium/scripts/deploy-governance.js		\
		build/conf/
	@(cd build; tar cfz metadium.tar.gz bin conf)
	@echo "Done building build/metadium.tar.gz"

gmet: rocksdb metadium/governance_abi.go metadium/governance_legacy_abi.go
ifeq ($(USE_ROCKSDB), NO)
	$(GORUN) build/ci.go install $(ROCKSDB_TAG) ./cmd/gmet
else
	CGO_CFLAGS="$(ROCKSDB_CGO_CFLAGS)" \
		CGO_LDFLAGS="$(ROCKSDB_CGO_LDFLAGS)" \
		$(GORUN) build/ci.go install $(ROCKSDB_TAG) ./cmd/gmet
endif
	@echo "Done building."
	@echo "Run \"$(GOBIN)/gmet\" to launch gmet."

logrot:
	$(GORUN) build/ci.go install ./cmd/logrot

geth:
	$(GORUN) build/ci.go install ./cmd/geth
	@echo "Done building."
	@echo "Run \"$(GOBIN)/geth\" to launch geth."

dbbench: rocksdb
ifeq ($(USE_ROCKSDB), NO)
	$(GORUN) build/ci.go install $(ROCKSDB_TAG) ./cmd/dbbench
else
	CGO_CFLAGS="$(ROCKSDB_CGO_CFLAGS)" \
		CGO_LDFLAGS="$(ROCKSDB_CGO_LDFLAGS)" \
		$(GORUN) build/ci.go install $(ROCKSDB_TAG) ./cmd/dbbench
endif

all: metadium/governance_abi.go metadium/governance_legacy_abi.go
	$(GORUN) build/ci.go install

android:
	$(GORUN) build/ci.go aar --local
	@echo "Done building."
	@echo "Import \"$(GOBIN)/geth.aar\" to use the library."
	@echo "Import \"$(GOBIN)/geth-sources.jar\" to add javadocs"
	@echo "For more info see https://stackoverflow.com/questions/20994336/android-studio-how-to-attach-javadoc"

ios:
	$(GORUN) build/ci.go xcode --local
	@echo "Done building."
	@echo "Import \"$(GOBIN)/Geth.framework\" to use the library."

test: all
	$(GORUN) build/ci.go test

test-short: all
	$(GORUN) build/ci.go test -short

lint: metadium/governance_abi.go metadium/governance_legacy_abi.go ## Run linters.
	$(GORUN) build/ci.go lint

fmt:
	gofmt -s -w $(shell find . -name "*.go")

clean:
	go clean -cache
	rm -fr build/_workspace/pkg/ $(GOBIN)/* build/conf metadium/governance_abi.go metadium/governance_legacy_abi.go
	@ROCKSDB_DIR=$(ROCKSDB_DIR);			\
	if [ -e $${ROCKSDB_DIR}/Makefile ]; then	\
		cd $${ROCKSDB_DIR};			\
		make clean;				\
	fi

# The devtools target installs tools required for 'go generate'.
# You need to put $GOBIN (or $GOPATH/bin) in your PATH to use 'go generate'.

devtools:
	env GOBIN= go install golang.org/x/tools/cmd/stringer@latest
	env GOBIN= go install github.com/fjl/gencodec@latest
	env GOBIN= go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	env GOBIN= go install ./cmd/abigen
	@type "solc" 2> /dev/null || echo 'Please install solc'
	@type "protoc" 2> /dev/null || echo 'Please install protoc'

gmet-linux:
	@if ! docker --version > /dev/null 2>&1; then			\
		echo "Docker not found. gmet-linux is the only supported"	\
		     "way to build release artifacts; see README." >&2;	\
		exit 1;							\
	fi
	docker build -t meta/builder:local -f Dockerfile.metadium .
	docker run -e HOME=/tmp --rm -v $(shell pwd):/data		\
		-u $(shell id -u):$(shell id -g)			\
		-w /data meta/builder:local				\
		"git config --global --add safe.directory /data;	\
		 make USE_ROCKSDB=$(GMET_LINUX_USE_ROCKSDB) STATIC_STDCPP=YES"
	@$(MAKE) --no-print-directory release-check

# Refuse artifacts that cannot run on the oldest distribution in the fleet.
# Checks every ELF in $(GOBIN), not just gmet: the bundle also ships logrot,
# which is built with cgo and carries a glibc floor of its own.
# This is the last gate before publishing, so silence must never read as a pass.
# Three ways it used to report OK without having checked anything:
#   - objdump errors went to /dev/null, so an unreadable file produced empty
#     output that was indistinguishable from a clean one ("GLIBC=none");
#   - a non-GNU objdump (llvm-objdump on macOS) satisfies `command -v` and then
#     prints a format this recipe does not parse;
#   - an empty $(GOBIN) checked zero binaries and still printed OK.
# Note that `objdump -T` legitimately fails on a statically linked binary
# ("not a dynamic object"), which is why readability is probed with -f and a
# missing dynamic symbol table is reported as such rather than as "none".
release-check:
	@command -v objdump > /dev/null 2>&1 || { echo "release-check: objdump not found" >&2; exit 1; }
	@objdump --version 2>/dev/null | head -1 | grep -q GNU || {		\
		echo "release-check: objdump is not GNU binutils, whose output this check parses" >&2; \
		echo "  found: `objdump --version 2>/dev/null | head -1`" >&2;	\
		exit 1;								\
	}
	@fail=0; checked=0;						\
	for f in $(GOBIN)/*; do						\
		[ -f "$$f" ] || continue;				\
		head -c 4 "$$f" | grep -q 'ELF' || continue;		\
		checked=`expr $$checked + 1`;				\
		if ! objdump -f "$$f" > /dev/null 2>&1; then		\
			echo "  $$f: FAIL: objdump cannot read this file" >&2; \
			fail=1; continue;				\
		fi;							\
		if syms=`objdump -T "$$f" 2>/dev/null`; then		\
			glibc=`echo "$$syms" | sed -n 's/.*GLIBC_\([0-9][0-9.]*\).*/\1/p' | sort -V | tail -1`; \
			gxx=`echo "$$syms" | grep -oE 'GLIBCXX_[0-9.]+' | sort -V | tail -1`; \
			cxxabi=`echo "$$syms" | grep -oE 'CXXABI_[0-9.]+' | sort -V | tail -1`; \
			echo "  $$f: GLIBC=$${glibc:-none} GLIBCXX=$${gxx:-none} CXXABI=$${cxxabi:-none}"; \
		else							\
			glibc=; gxx=; cxxabi=;				\
			echo "  $$f: no dynamic symbol table (statically linked)"; \
		fi;							\
		if [ -n "$$glibc" ] && [ "`printf '%s\n%s\n' "$$glibc" "$(MAX_GLIBC)" | sort -V | tail -1`" != "$(MAX_GLIBC)" ]; then \
			echo "    FAIL: needs GLIBC_$$glibc > $(MAX_GLIBC)" >&2; fail=1; \
		fi;							\
		if [ -n "$$gxx" ] || [ -n "$$cxxabi" ]; then		\
			echo "    FAIL: links libstdc++ dynamically (build with STATIC_STDCPP=YES)" >&2; fail=1; \
		fi;							\
		objdump -p "$$f" 2>/dev/null | awk '/NEEDED/ {printf "    NEEDED %s\n", $$2}'; \
	done;								\
	if [ $$checked = 0 ]; then					\
		echo "release-check: FAILED — no ELF binary found in $(GOBIN), nothing was checked" >&2; \
		exit 1;							\
	fi;								\
	if [ $$fail != 0 ]; then					\
		echo "release-check: FAILED — do not publish these artifacts" >&2; \
		exit 1;							\
	fi;								\
	echo "release-check: OK ($$checked binaries, ceiling GLIBC_$(MAX_GLIBC))"
	@echo "  NEEDED entries above must be present on target hosts (snappy, lz4, zstd, jemalloc)."

ifneq ($(USE_ROCKSDB), YES)
rocksdb:
else
rocksdb:
	@[ ! -e rocksdb/.git ] && git submodule update --init rocksdb;	\
	cd $(ROCKSDB_DIR) && PORTABLE=1 make -j8 static_lib;
endif

AWK_CODE='								     \
BEGIN { print "package metadium\n"; }					     \
/^var Registry_contract/ {						     \
  sub("^var[^(]*\\(","",$$0); sub("\\);$$","",$$0);			     \
  n = "Registry";							     \
  print "var " n "Abi = `{ \"contractName\": \"" n "\", \"abi\": " $$0 "}`"; \
}									     \
/^var StakingImp_contract/ {						     \
  sub("^var[^(]*\\(","",$$0); sub("\\);$$","",$$0);			     \
  n = "Staking";							     \
  print "var " n "Abi = `{ \"contractName\": \"" n "\", \"abi\": " $$0 "}`"; \
}									     \
/^var EnvStorageImp_contract/ {						     \
  sub("^var[^(]*\\(","",$$0); sub("\\);$$","",$$0);			     \
  n = "EnvStorageImp";							     \
  print "var " n "Abi = `{ \"contractName\": \"" n "\", \"abi\": " $$0 "}`"; \
}									     \
/^var GovImp_contract/ {							     \
  sub("^var[^(]*\\(","",$$0); sub("\\);$$","",$$0);			     \
  n = "Gov";								     \
  print "var " n "Abi = `{ \"contractName\": \"" n "\", \"abi\": " $$0 "}`"; \
}									     \
/^var TRSListImp_contract/ {							     \
  sub("^var[^(]*\\(","",$$0); sub("\\);$$","",$$0);			     \
  n = "TRSList";								     \
  print "var " n "Abi = `{ \"contractName\": \"" n "\", \"abi\": " $$0 "}`"; \
}'

metadium/governance_abi.go: metadium/contracts/MetadiumGovernance.js
	@cat $< | awk $(AWK_CODE) > $@

AWK_CODE_LEGACY='								     \
BEGIN { print "package metadium\n"; }					     \
/^var Registry_contract/ {						     \
  sub("^var[^(]*\\(","",$$0); sub("\\);$$","",$$0);			     \
  n = "Registry";							     \
  print "var " n "LegacyAbi = `{ \"contractName\": \"" n "\", \"abi\": " $$0 "}`"; \
}									     \
/^var Staking_contract/ {						     \
  sub("^var[^(]*\\(","",$$0); sub("\\);$$","",$$0);			     \
  n = "Staking";							     \
  print "var " n "LegacyAbi = `{ \"contractName\": \"" n "\", \"abi\": " $$0 "}`"; \
}									     \
/^var EnvStorageImp_contract/ {						     \
  sub("^var[^(]*\\(","",$$0); sub("\\);$$","",$$0);			     \
  n = "EnvStorageImp";							     \
  print "var " n "LegacyAbi = `{ \"contractName\": \"" n "\", \"abi\": " $$0 "}`"; \
}									     \
/^var Gov_contract/ {							     \
  sub("^var[^(]*\\(","",$$0); sub("\\);$$","",$$0);			     \
  n = "Gov";								     \
  print "var " n "LegacyAbi = `{ \"contractName\": \"" n "\", \"abi\": " $$0 "}`"; \
}'

metadium/governance_legacy_abi.go: metadium/contracts/MetadiumGovernanceLegacy.js
	@cat $< | awk $(AWK_CODE_LEGACY) > $@


ifneq ($(shell uname), Linux)

build/bin/solc:
	@test 1

else

SOLC_URL=https://github.com/ethereum/solidity/releases/download/v0.4.24/solc-static-linux
build/bin/solc:
	@[ -d build/bin ] || mkdir -p build/bin;		\
	if [ ! -x build/bin/solc ]; then			\
		if which curl > /dev/null 2>&1; then		\
			curl -Ls -o build/bin/solc $(SOLC_URL);	\
			chmod +x build/bin/solc;		\
		elif which wget > /dev/null 2>&1; then		\
			wget -nv -o build/bin/solc $(SOLC_URL);	\
			chmod +x build/bin/solc;		\
		fi						\
	fi

endif

help: Makefile
	@echo ''
	@echo 'Usage:'
	@echo '  make [target]'
	@echo ''
	@echo 'Targets:'
	@sed -n 's/^#?//p' $< | column -t -s ':' |  sort | sed -e 's/^/ /'
