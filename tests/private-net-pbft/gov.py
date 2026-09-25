#!/usr/bin/env python3
"""gov.py - governance ballots on the private network, for §11.2 S-08/S-15.

  ./gov.py nodes            node and member counts, and the ballot in voting
  ./gov.py remove N         propose removing node N's member (from node1),
                            then vote yes from every member in turn until
                            the ballot is decided or everyone has voted
  ./gov.py vote BALLOT      the voting part alone, for a ballot in voting
  ./gov.py finalize         end a ballot whose voting period is over
  ./gov.py add N            propose adding node N back as a member, and vote

Votes are sent from node1, the one node that allows unlocking over HTTP:
the members' test keys (setup.sh) are imported into its keystore first.
Selectors come from the node's web3_sha3: the standard library has no
keccak.
"""
import json, re, sys, time, urllib.request

PASSWORD = "privatenet123"


def rpc(port, method, params=None):
    body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params or []}).encode()
    req = urllib.request.Request(f"http://localhost:{port}", body, {"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=15) as r:
        d = json.load(r)
    if "error" in d:
        raise RuntimeError(f"{method}: {d['error']}")
    return d["result"]


def nodes():
    with open("node-ids.txt") as f:
        return [line.split() for line in f]  # [name, node id, account]


def port(n):
    return 8644 + n


def selector(sig):
    return rpc(port(1), "web3_sha3", ["0x" + sig.encode().hex()])[2:10]


def word(v):
    if isinstance(v, bool):
        v = int(v)
    if isinstance(v, str):  # address
        return v[2:].lower().rjust(64, "0")
    return format(v, "064x")


def encode(sig, *args):
    """ABI-encodes static words and one trailing-dynamic bytes argument."""
    head, tail = [], ""
    n = len(args)
    for a in args:
        if isinstance(a, bytes):
            head.append(format(32 * n + len(tail) // 2, "064x"))
            data = a.hex()
            tail += format(len(a), "064x") + data.ljust((len(data) + 63) // 64 * 64, "0")
        else:
            head.append(word(a))
    return "0x" + selector(sig) + "".join(head) + tail


def call(to, sig, *args):
    return rpc(port(1), "eth_call", [{"to": to, "data": encode(sig, *args)}, "latest"])


def gov_address():
    log = open("data/deploy.log").read()
    registry = re.search(r'"REGISTRY_ADDRESS":\s*"(0x[0-9a-fA-F]{40})"', log).group(1)
    name = b"GovernanceContract".ljust(32, b"\0").hex()
    out = rpc(port(1), "eth_call", [{"to": registry, "data": "0x" + selector("getContractAddress(bytes32)") + name}, "latest"])
    return "0x" + out[-40:]


def keys():
    return re.findall(r'^\s*"([0-9a-f]{64})"$', open("setup.sh").read(), re.M)


def send(n, to, data):
    """Sends data from node n's member account, signed on node1."""
    acc = nodes()[n - 1][2]
    if acc.lower() not in [a.lower() for a in rpc(port(1), "personal_listAccounts")]:
        rpc(port(1), "personal_importRawKey", [keys()[n - 1], PASSWORD])
    rpc(port(1), "personal_unlockAccount", [acc, PASSWORD, 60])
    h = rpc(port(1), "eth_sendTransaction", [{"from": acc, "to": to, "data": data, "gas": hex(3_000_000)}])
    for _ in range(120):
        r = rpc(port(1), "eth_getTransactionReceipt", [h])
        if r:
            return int(r["status"], 16), int(r["blockNumber"], 16)
        time.sleep(0.5)
    return None, None  # never committed


def counts(gov):
    return (int(call(gov, "getNodeLength()"), 16), int(call(gov, "getMemberLength()"), 16),
            int(call(gov, "getBallotInVoting()"), 16))


def remove(n):
    gov = gov_address()
    staker = nodes()[n - 1][2]
    duration = int(call(gov, "getMinVotingDuration()"), 16)
    before = counts(gov)
    print(f"before: {before[0]} nodes, {before[1]} members, ballot in voting {before[2]}")
    status, block = send(1, gov, encode("addProposalToRemoveMember(address,uint256,bytes,uint256)",
                                        staker, 10**18, b"remove node%d" % n, duration))
    print(f"proposal to remove node{n}: status {status} in block {block}")
    return vote(int(call(gov, "ballotLength()"), 16), before[1])


def vote(ballot, members_before=None):
    """Votes yes on ballot from every member in turn until the member count
    drops (the removal is decided) or everyone has voted."""
    gov = gov_address()
    if members_before is None:
        members_before = counts(gov)[1]
    for voter in range(1, len(nodes()) + 1):
        if counts(gov)[1] < members_before:
            break
        status, block = send(voter, gov, encode("vote(uint256,bool)", ballot, True))
        print(f"  node{voter} votes on ballot {ballot}: status {status} in block {block}")
        if status is None:
            print("  the vote was never committed")
            break
    after = counts(gov)
    print(f"after: {after[0]} nodes, {after[1]} members, ballot in voting {after[2]}")
    return after


def encode_member(staker, name, enode, ip, port_, lock, memo, duration):
    """addProposalToAddMember(MemberInfo): one dynamic tuple argument."""
    sig = "addProposalToAddMember((address,address,address,bytes,bytes,bytes,uint256,uint256,bytes,uint256))"
    fields = [staker, staker, staker, name, enode, ip, port_, lock, memo, duration]
    head, tail = [], ""
    for f in fields:
        if isinstance(f, bytes):
            head.append(format(32 * len(fields) + len(tail) // 2, "064x"))
            data = f.hex()
            tail += format(len(f), "064x") + data.ljust((len(data) + 63) // 64 * 64, "0")
        else:
            head.append(word(f))
    return "0x" + selector(sig) + format(32, "064x") + "".join(head) + tail


def finalize():
    """Ends a ballot whose voting period is over (finalizeEndedVote)."""
    gov = gov_address()
    status, block = send(1, gov, encode("finalizeEndedVote()"))
    print(f"finalizeEndedVote: status {status} in block {block}; ballot in voting now {counts(gov)[2]}")


def add(n):
    gov = gov_address()
    name, nid, acc = nodes()[n - 1]
    duration = int(call(gov, "getMinVotingDuration()"), 16)
    before = counts(gov)
    print(f"before: {before[0]} nodes, {before[1]} members, ballot in voting {before[2]}")
    data = encode_member(acc, name.encode(), bytes.fromhex(nid), b"172.32.0.1%d" % n, 30303, 10**18,
                         b"add node%d" % n, duration)
    status, block = send(1, gov, data)
    print(f"proposal to add node{n}: status {status} in block {block}")
    ballot = int(call(gov, "ballotLength()"), 16)
    for voter in range(1, len(nodes()) + 1):
        if counts(gov)[1] > before[1]:
            break
        if voter == n:
            continue  # not a member yet
        status, block = send(voter, gov, encode("vote(uint256,bool)", ballot, True))
        print(f"  node{voter} votes on ballot {ballot}: status {status} in block {block}")
    after = counts(gov)
    print(f"after: {after[0]} nodes, {after[1]} members, ballot in voting {after[2]}")


if __name__ == "__main__":
    if sys.argv[1:2] == ["nodes"]:
        print("nodes %d, members %d, ballot in voting %d" % counts(gov_address()))
    elif sys.argv[1:2] == ["remove"]:
        remove(int(sys.argv[2]))
    elif sys.argv[1:2] == ["vote"]:
        vote(int(sys.argv[2]))
    elif sys.argv[1:2] == ["add"]:
        add(int(sys.argv[2]))
    elif sys.argv[1:2] == ["finalize"]:
        finalize()
    else:
        print(__doc__)
        sys.exit(2)
