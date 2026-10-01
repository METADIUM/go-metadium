#!/usr/bin/env python3
# test_check_camellia_headers.py - regression cover for check-camellia-headers.py
# (issue #141).
#
# The checker certifies history before a C1 header rule is deployed, and its
# failure mode is silent: a rule that stops firing, or a mirrored constant that
# drifts from the Go source, still ends in "violations=0". So each test below
# feeds a header that breaks exactly one rule and asserts the checker reports
# it; removing that rule from the checker makes its test fail. A second group
# asserts the mirrored constants still equal what the Go source says.
#
# Standard library only, no node needed:
#   python3 scripts/test_check_camellia_headers.py

import importlib.util
import os
import re
import sys
import unittest

# Importing the checker by path would otherwise leave scripts/__pycache__ behind.
sys.dont_write_bytecode = True

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)

_spec = importlib.util.spec_from_file_location(
    "check_camellia_headers", os.path.join(HERE, "check-camellia-headers.py"))
chk = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(chk)


def header(excess=0, used=0, withdrawals=None, beacon=None, drop=()):
    """A post-Camellia header as eth_getBlockByNumber returns it."""
    h = {
        "withdrawalsRoot": chk.EMPTY_WITHDRAWALS if withdrawals is None else withdrawals,
        "excessBlobGas": hex(excess),
        "blobGasUsed": hex(used),
    }
    if beacon is not None:
        h["parentBeaconBlockRoot"] = beacon
    for name in drop:
        h.pop(name)
    return h


PARENT = header()


class RuleTest(unittest.TestCase):
    def assertFlags(self, block, parent, needle):
        problems = chk.check_header(block, parent)
        self.assertEqual(len(problems), 1, problems)
        self.assertIn(needle, problems[0])

    def test_clean_header_passes(self):
        self.assertEqual(chk.check_header(header(), PARENT), [])
        # One blob after an empty parent, and the excess it leaves for the child.
        one = header(used=chk.PER_BLOB)
        self.assertEqual(chk.check_header(one, PARENT), [])
        two = header(used=2 * chk.PER_BLOB)
        self.assertEqual(chk.check_header(two, PARENT), [])
        child = header(excess=chk.calc_excess_blob_gas(0, 2 * chk.PER_BLOB))
        self.assertEqual(chk.check_header(child, two), [])

    def test_withdrawals_root_not_empty(self):
        self.assertFlags(header(withdrawals="0x" + "11" * 32), PARENT, "withdrawalsRoot")

    def test_withdrawals_root_missing(self):
        self.assertFlags(header(drop=("withdrawalsRoot",)), PARENT, "missing withdrawalsRoot")

    def test_excess_blob_gas_not_derived_from_parent(self):
        self.assertFlags(header(excess=1), PARENT, "excessBlobGas")
        # Non-trivial parent: the child must carry the derived excess, not zero.
        parent = header(used=2 * chk.PER_BLOB)
        self.assertNotEqual(chk.calc_excess_blob_gas(0, 2 * chk.PER_BLOB), 0)
        self.assertFlags(header(excess=0), parent, "excessBlobGas")

    def test_excess_blob_gas_missing(self):
        self.assertFlags(header(drop=("excessBlobGas",)), PARENT, "missing excessBlobGas")

    def test_blob_gas_used_missing(self):
        self.assertFlags(header(drop=("blobGasUsed",)), PARENT, "missing blobGasUsed")

    def test_parent_beacon_root_present(self):
        self.assertFlags(header(beacon="0x" + "00" * 32), PARENT, "parentBeaconBlockRoot")

    def test_blob_gas_used_over_max(self):
        # A whole multiple, so only the cap fires.
        self.assertFlags(header(used=3 * chk.PER_BLOB), PARENT, "over the per-block max")

    def test_blob_gas_used_not_a_multiple(self):
        # Under the cap, so only the multiple fires.
        self.assertFlags(header(used=chk.PER_BLOB + 1), PARENT, "not a multiple")


def go_constants(path):
    """Evaluate the `NAME = <int expr>` constants in a Go file that the checker mirrors."""
    src = open(os.path.join(ROOT, path)).read()
    values = {}
    for name, expr in re.findall(r"^\s*(\w+)\s*=\s*([0-9<>*\s\w]+?)\s*(?://.*)?$", src, re.M):
        expr = expr.strip()
        try:
            values[name] = eval(expr, {"__builtins__": {}}, dict(values))
        except Exception:
            pass
    return values


class MirrorTest(unittest.TestCase):
    def test_blob_gas_constants_match_params(self):
        go = go_constants("params/protocol_params.go")
        self.assertEqual(chk.PER_BLOB, go["BlobTxBlobGasPerBlob"])
        self.assertEqual(chk.TARGET_BLOB_GAS, go["BlobTxTargetBlobGasPerBlock"])
        self.assertEqual(chk.MAX_BLOB_GAS, go["MaxBlobGasPerBlock"])

    def test_empty_withdrawals_hash_matches_types(self):
        src = open(os.path.join(ROOT, "core/types/hashes.go")).read()
        m = re.search(r'EmptyWithdrawalsHash\s*=\s*common\.HexToHash\("([0-9a-fA-F]{64})"\)', src)
        self.assertIsNotNone(m, "EmptyWithdrawalsHash not found in core/types/hashes.go")
        self.assertEqual(chk.EMPTY_WITHDRAWALS, "0x" + m.group(1).lower())


if __name__ == "__main__":
    unittest.main()
