// SPDX-License-Identifier: MIT
pragma solidity ^0.8.28;

/// Exercises compiler-emitted Shanghai/Cancun opcodes:
/// - PUSH0 (0x5f): emitted by default for zero constants (Shanghai+)
/// - MCOPY (0x5e): emitted for memory-to-memory copies (Cancun, 0.8.25+)
/// - TSTORE/TLOAD (0x5d/0x5c): via `transient` keyword (0.8.28)
contract Camellia {
    uint256 public x;
    uint256 transient t;

    function set(uint256 v) external { x = v; }
    function inc() external { x += 1; }

    // bytes memory return triggers MCOPY-based memory copy on 0.8.25+/cancun
    function echo(bytes calldata d) external pure returns (bytes memory) {
        return d;
    }

    // transient storage: written and read back within the same call
    function txn(uint256 v) external returns (uint256 sameCall) {
        t = v;
        return t;
    }
}
