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
        # Two blobs over a target of one leave 131072 for the child. A literal,
        # so the formula is compared with a number it did not produce.
        child = header(excess=131072)
        self.assertEqual(chk.check_header(child, two), [])

    def test_withdrawals_root_not_empty(self):
        self.assertFlags(header(withdrawals="0x" + "11" * 32), PARENT, "withdrawalsRoot")

    def test_withdrawals_root_missing(self):
        self.assertFlags(header(drop=("withdrawalsRoot",)), PARENT, "missing withdrawalsRoot")

    def test_excess_blob_gas_not_derived_from_parent(self):
        self.assertFlags(header(excess=1), PARENT, "excessBlobGas")
        # Non-trivial parent: the child must carry the derived excess, not zero.
        parent = header(used=2 * chk.PER_BLOB)
        self.assertFlags(header(excess=0), parent, "excessBlobGas")

    def test_excess_blob_gas_carries_parent_excess(self):
        # The parent's own excess counts, not only its blobGasUsed: with excess
        # 131072 and one blob, 131072 + 131072 - 131072 = 131072 for the child.
        # Every expected value is a literal, so a formula that drops
        # parent_excess, or reads it as zero, is caught here.
        parent = header(excess=131072, used=131072)
        self.assertEqual(chk.check_header(header(excess=131072), parent), [])
        self.assertFlags(header(excess=0), parent, "excessBlobGas 0, want 131072")
        self.assertFlags(header(excess=262144), parent, "excessBlobGas 262144, want 131072")
        # Parent at the cap with excess already carried: 262144 + 262144 - 131072.
        parent = header(excess=262144, used=262144)
        self.assertEqual(chk.check_header(header(excess=393216), parent), [])
        self.assertFlags(header(excess=262144), parent, "want 393216")

    def test_excess_blob_gas_missing(self):
        self.assertFlags(header(drop=("excessBlobGas",)), PARENT, "missing excessBlobGas")

    def test_blob_gas_used_missing(self):
        self.assertFlags(header(drop=("blobGasUsed",)), PARENT, "missing blobGasUsed")

    def test_parent_beacon_root_present(self):
        self.assertFlags(header(beacon="0x" + "00" * 32), PARENT, "parentBeaconBlockRoot")

    def test_blob_gas_used_over_max(self):
        # One blob past the cap: a whole multiple, so only the cap fires, and it
        # keeps testing the cap if the max changes.
        self.assertFlags(header(used=chk.MAX_BLOB_GAS + chk.PER_BLOB), PARENT,
                         "over the per-block max")

    def test_blob_gas_used_not_a_multiple(self):
        # Under the cap, so only the multiple fires.
        self.assertFlags(header(used=chk.PER_BLOB + 1), PARENT, "not a multiple")


class FormulaTest(unittest.TestCase):
    def test_excess_blob_gas_vectors(self):
        # types.CalcExcessBlobGas with Metadium's target of one blob (131072)
        # and max of two. Literal inputs and outputs: none of them goes through
        # the module's constants or its own formula, so a target that drifts
        # (the #131/#136 shape: right constant, wrong number in the formula) or
        # a term that is dropped shows up as a wrong number here.
        vectors = [
            # (parent excess, parent used) -> child excess
            ((0, 0), 0),
            ((0, 131072), 0),            # one blob meets the target exactly
            ((0, 262144), 131072),       # two blobs: one over the target
            ((131072, 0), 0),            # carried excess, no blobs: back to zero
            ((262144, 0), 131072),       # carried excess decays by one target
            ((131072, 131072), 131072),  # carried excess plus one blob holds
            ((131072, 262144), 262144),  # carried excess plus two blobs grows
            ((262144, 262144), 393216),
            ((393216, 0), 262144),
        ]
        for (parent_excess, parent_used), want in vectors:
            self.assertEqual(chk.calc_excess_blob_gas(parent_excess, parent_used), want,
                             f"calc_excess_blob_gas({parent_excess}, {parent_used})")


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
