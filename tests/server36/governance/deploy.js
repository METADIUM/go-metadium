#!/usr/bin/env node
/**
 * deploy.js - Deploy Metadium governance contracts to private PoA network
 *
 * Prerequisites:
 *   - governance-contract repo compiled: GOV_DIR must have artifacts/
 *   - Private network running on RPC_URL (localhost:8545)
 *   - Deployer account (DEPLOYER_ADDR) nonce must be 0 (fresh chain)
 *     because Registry address = crypto.CreateAddress(deployer, 0)
 *
 * Usage:
 *   GOV_DIR=/tmp/gov-contract RPC_URL=http://localhost:8545 node deploy.js
 *
 * Registry domain names used by gmet (admin.go):
 *   "GovernanceContract" → Gov proxy
 *   "EnvStorage"         → EnvStorage proxy
 *   "Staking"            → Staking
 *   "StakingReward"      → reward pool for stakers
 *   "Ecosystem"          → ecosystem fund
 *   "Maintenance"        → maintenance fund
 *   "FeeCollector"       → tx fee collector (optional)
 */

"use strict";

const { ethers } = require("ethers");
const fs = require("fs");
const path = require("path");

// ─── Config ─────────────────────────────────────────────────────────────────
const RPC_URL   = process.env.RPC_URL   || "http://localhost:8545";
const GOV_DIR   = process.env.GOV_DIR   || "/tmp/gov-contract";
const CHAIN_ID  = parseInt(process.env.CHAIN_ID || "1338");

// Well-known Hardhat account private keys (accounts pre-funded in genesis)
const PRIVKEYS = [
  "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80", // node1 → 0xf39F...
  "0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d", // node2 → 0x7099...
  "0x5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a", // node3 → 0x3C44...
];

// Node info (from static-nodes.json / setup.sh)
// Enode public keys derived from nodekeys via admin_nodeInfo
const NODES = [
  {
    name: "node1",
    enode: "bc87c4add6cbf964621dbc2d43ea18f879d4e406daab15b07c8bfef5cf992c7a08a1ca1df07c56f0e37333785e5439839bb634c8523f13e902b00d498954e3ba",
    ip:   "172.32.0.11",
    port: 30303,
  },
  {
    name: "node2",
    enode: "7bcf4a76d77fe3935cb0ce272be886346d9b5ed7b0b728c2fc4ef1fb41d01cf412c93fbac8310f56d1d98ea6fec6aba311dcd6e53651ae3e5e117d5a711e709e",
    ip:   "172.32.0.12",
    port: 30303,
  },
  {
    name: "node3",
    enode: "0d5512ff30a03d1db611624cb1058cc25f8374e4677a057c061a73c9c0998c125acd09f6a25e79a770c38880fae3aae52fab50d632e0b12d82e2e11254f14f0e",
    ip:   "172.32.0.13",
    port: 30303,
  },
];

// Governance parameters
const LOCK_AMOUNT  = ethers.parseEther("1000");     // 1,000 META stake per member
const MIN_STAKING  = ethers.parseEther("100");      // 100 META minimum
const MAX_STAKING  = ethers.parseEther("1000000");  // 1M META maximum
const BLOCK_REWARD = ethers.parseEther("1");        // 1 META per block (minting)
const BLOCKS_PER   = 100n;                          // blocks per reward period

// Distribution: must sum to 10000 (basis points)
// [blockProducer, stakingReward, ecosystem, maintenance]
const DISTRIBUTION = [4000n, 1000n, 2500n, 2500n];

// ─── Artifact loader ─────────────────────────────────────────────────────────
function loadArtifact(solFile, contractName) {
  const p = path.join(GOV_DIR, "artifacts", "contracts", solFile, contractName + ".json");
  if (!fs.existsSync(p)) {
    throw new Error(`Artifact not found: ${p}\nRun 'npx hardhat compile' in ${GOV_DIR}`);
  }
  const a = JSON.parse(fs.readFileSync(p, "utf8"));
  return { abi: a.abi, bytecode: a.bytecode };
}

// ─── Helpers ─────────────────────────────────────────────────────────────────
function toBytes32(str) {
  return ethers.encodeBytes32String(str.slice(0, 31)); // right-padded with zeros
}

function toBytes(str) {
  return ethers.toUtf8Bytes(str);
}

function log(msg) {
  const ts = new Date().toISOString().slice(11, 19);
  console.log(`[${ts}] ${msg}`);
}

// ─── Main ────────────────────────────────────────────────────────────────────
async function main() {
  const provider = new ethers.JsonRpcProvider(RPC_URL);
  const network = await provider.getNetwork();
  log(`Connected to chain ${network.chainId} via ${RPC_URL}`);

  const deployer = new ethers.Wallet(PRIVKEYS[0], provider);
  const signers  = PRIVKEYS.map(k => new ethers.Wallet(k, provider));

  const deployerNonce = await provider.getTransactionCount(deployer.address);
  log(`Deployer: ${deployer.address}  nonce=${deployerNonce}`);
  if (deployerNonce > 9) {
    throw new Error(
      `Deployer nonce=${deployerNonce} — Registry will not be found by gmet.\n` +
      `Start a fresh network (stop.sh --clean + setup.sh + start_network) and deploy governance FIRST.`
    );
  }
  if (deployerNonce > 0) {
    log(`WARN: nonce=${deployerNonce} — Registry will be at CreateAddress(deployer, ${deployerNonce}), not 0. gmet checks 0..9 so this still works.`);
  }

  // ── 1. Registry ──────────────────────────────────────────────────────────
  log("Deploying Registry...");
  const registryArt = loadArtifact("Registry.sol", "Registry");
  const RegistryF   = new ethers.ContractFactory(registryArt.abi, registryArt.bytecode, deployer);
  const registry    = await RegistryF.deploy();
  await registry.waitForDeployment();
  const registryAddr = await registry.getAddress();
  log(`  Registry: ${registryAddr}`);

  // ── 2. Staking ───────────────────────────────────────────────────────────
  log("Deploying StakingImp (implementation)...");
  const stakingImpArt = loadArtifact("StakingImp.sol", "StakingImp");
  const StakingImpF   = new ethers.ContractFactory(stakingImpArt.abi, stakingImpArt.bytecode, deployer);
  const stakingImp    = await StakingImpF.deploy();
  await stakingImp.waitForDeployment();
  const stakingImpAddr = await stakingImp.getAddress();
  log(`  StakingImp: ${stakingImpAddr}`);

  log("Deploying Staking proxy...");
  const stakingArt = loadArtifact("storage/Staking.sol", "Staking");
  const StakingF   = new ethers.ContractFactory(stakingArt.abi, stakingArt.bytecode, deployer);
  const staking    = await StakingF.deploy(stakingImpAddr);
  await staking.waitForDeployment();
  const stakingAddr = await staking.getAddress();
  log(`  Staking proxy: ${stakingAddr}`);

  // ── 3. BallotStorage ─────────────────────────────────────────────────────
  log("Deploying BallotStorage...");
  const ballotArt = loadArtifact("storage/BallotStorage.sol", "BallotStorage");
  const BallotF   = new ethers.ContractFactory(ballotArt.abi, ballotArt.bytecode, deployer);
  const ballot    = await BallotF.deploy(registryAddr);
  await ballot.waitForDeployment();
  const ballotAddr = await ballot.getAddress();
  log(`  BallotStorage: ${ballotAddr}`);

  // ── 4. EnvStorageImp (implementation) ────────────────────────────────────
  log("Deploying EnvStorageImp...");
  const envImpArt = loadArtifact("storage/EnvStorageImp.sol", "EnvStorageImp");
  const EnvImpF   = new ethers.ContractFactory(envImpArt.abi, envImpArt.bytecode, deployer);
  const envImp    = await EnvImpF.deploy();
  await envImp.waitForDeployment();
  const envImpAddr = await envImp.getAddress();
  log(`  EnvStorageImp: ${envImpAddr}`);

  // ── 5. EnvStorage (proxy) ────────────────────────────────────────────────
  log("Deploying EnvStorage proxy...");
  const envArt = loadArtifact("storage/EnvStorage.sol", "EnvStorage");
  const EnvF   = new ethers.ContractFactory(envArt.abi, envArt.bytecode, deployer);
  const env    = await EnvF.deploy(envImpAddr);
  await env.waitForDeployment();
  const envAddr = await env.getAddress();
  log(`  EnvStorage: ${envAddr}`);

  // ── 6. GovImp (implementation) ───────────────────────────────────────────
  log("Deploying GovImp...");
  const govImpArt = loadArtifact("GovImp.sol", "GovImp");
  const GovImpF   = new ethers.ContractFactory(govImpArt.abi, govImpArt.bytecode, deployer);
  const govImp    = await GovImpF.deploy();
  await govImp.waitForDeployment();
  const govImpAddr = await govImp.getAddress();
  log(`  GovImp: ${govImpAddr}`);

  // ── 7. Gov (proxy) ───────────────────────────────────────────────────────
  log("Deploying Gov proxy...");
  const govArt = loadArtifact("Gov.sol", "Gov");
  const GovF   = new ethers.ContractFactory(govArt.abi, govArt.bytecode, deployer);
  const gov    = await GovF.deploy(govImpAddr);
  await gov.waitForDeployment();
  const govAddr = await gov.getAddress();
  log(`  Gov: ${govAddr}`);

  // ── 8. Register all contracts in Registry ────────────────────────────────
  log("Registering contracts in Registry...");
  const rewardPoolAddr = deployer.address; // node1 gets StakingReward (for this test)
  const ecosystemAddr  = signers[1].address; // node2 gets Ecosystem
  const maintenanceAddr = signers[2].address; // node3 gets Maintenance

  const domains = [
    ["GovernanceContract", govAddr],
    ["Staking",            stakingAddr],
    ["BallotStorage",      ballotAddr],
    ["EnvStorage",         envAddr],
    ["StakingReward",      rewardPoolAddr],
    ["Ecosystem",          ecosystemAddr],
    ["Maintenance",        maintenanceAddr],
  ];
  for (const [name, addr] of domains) {
    try {
      const tx = await registry.setContractDomain(toBytes32(name), addr);
      await tx.wait();
      log(`  setContractDomain("${name}") → ${addr}`);
    } catch (e) {
      log(`  WARN: setContractDomain("${name}") failed: ${e.message.slice(0, 120)}`);
      // Retry once
      try {
        await new Promise(r => setTimeout(r, 2000));
        const tx2 = await registry.setContractDomain(toBytes32(name), addr);
        await tx2.wait();
        log(`  setContractDomain("${name}") → ${addr} (retry ok)`);
      } catch (e2) {
        log(`  ERROR: setContractDomain("${name}") failed after retry: ${e2.message.slice(0, 120)}`);
      }
    }
  }

  // ── 9. Initialize EnvStorage with governance params ──────────────────────
  log("Initializing EnvStorage parameters...");
  // EnvStorage proxy with EnvStorageImp ABI
  const envProxy = new ethers.Contract(envAddr, envImpArt.abi, deployer);

  // Named-key approach: keys are keccak256 of the name string
  const envKeys = [
    ethers.keccak256(ethers.toUtf8Bytes("blocksPer")),
    ethers.keccak256(ethers.toUtf8Bytes("ballotDurationMin")),
    ethers.keccak256(ethers.toUtf8Bytes("ballotDurationMax")),
    ethers.keccak256(ethers.toUtf8Bytes("stakingMin")),
    ethers.keccak256(ethers.toUtf8Bytes("stakingMax")),
    ethers.keccak256(ethers.toUtf8Bytes("blockRewardAmount")),
    ethers.keccak256(ethers.toUtf8Bytes("blockRewardDistributionBlockProducer")),
    ethers.keccak256(ethers.toUtf8Bytes("blockRewardDistributionStakingReward")),
    ethers.keccak256(ethers.toUtf8Bytes("blockRewardDistributionEcosystem")),
    ethers.keccak256(ethers.toUtf8Bytes("blockRewardDistributionMaintenance")),
    ethers.keccak256(ethers.toUtf8Bytes("blockCreationTime")),
    ethers.keccak256(ethers.toUtf8Bytes("MaxIdleBlockInterval")),
  ];
  const envVals = [
    BLOCKS_PER,          // blocksPer
    86400n,              // ballotDurationMin (1 day)
    604800n,             // ballotDurationMax (7 days)
    MIN_STAKING,         // stakingMin
    MAX_STAKING,         // stakingMax
    BLOCK_REWARD,        // blockRewardAmount
    DISTRIBUTION[0],     // blockProducer share
    DISTRIBUTION[1],     // stakingReward share
    DISTRIBUTION[2],     // ecosystem share
    DISTRIBUTION[3],     // maintenance share
    2000n,               // blockCreationTime (ms) - matches our ~2s blocks
    5n,                  // MaxIdleBlockInterval
  ];

  try {
    const tx = await envProxy.initialize(registryAddr, envKeys, envVals);
    await tx.wait();
    log("  EnvStorage initialized");
  } catch (e) {
    log(`  EnvStorage initialize failed (${e.message}), trying setAll...`);
    // Fallback: try setBlockRewardAmount individually
    try {
      const tx1 = await envProxy.setBlockRewardAmount(BLOCK_REWARD);
      await tx1.wait();
      const tx2 = await envProxy.setBlockRewardDistributionMethod(...DISTRIBUTION);
      await tx2.wait();
      log("  Set blockRewardAmount and distribution via setters");
    } catch (e2) {
      log(`  WARNING: Could not set env params: ${e2.message}`);
    }
  }

  // ── 10. Initialize Staking proxy with registry ───────────────────────────
  log("Initializing Staking proxy with registry...");
  const stakingProxy = new ethers.Contract(stakingAddr, stakingImpArt.abi, deployer);
  try {
    const txInit = await stakingProxy.init(registryAddr, "0x");
    await txInit.wait();
    log("  Staking.init() done");
  } catch (e) {
    log(`  Staking.init() skipped (${e.message.slice(0, 60)})`);
  }

  // ── 11. Deposit stake for all 3 miners ────────────────────────────────────
  log("Depositing stake for all miners...");
  for (let i = 0; i < signers.length; i++) {
    const stakingAs = new ethers.Contract(stakingAddr, stakingImpArt.abi, signers[i]);
    try {
      const txDep = await stakingAs.deposit({ value: LOCK_AMOUNT });
      await txDep.wait();
      log(`  ${NODES[i].name} (${signers[i].address}) deposited ${ethers.formatEther(LOCK_AMOUNT)} META`);
    } catch (e) {
      log(`  ${NODES[i].name} stake failed: ${e.message.slice(0, 80)}`);
    }
  }

  // ── 12. Initialize Gov with all 3 miners via initOnce ─────────────────────
  log("Initializing Gov with 3 members via initOnce...");
  const govProxy = new ethers.Contract(govAddr, govImpArt.abi, deployer);

  function addr32(a) {
    return ethers.getBytes(ethers.zeroPadValue(a, 32));
  }
  function uint32b(n) {
    return ethers.getBytes(ethers.zeroPadValue(ethers.toBeHex(BigInt(n)), 32));
  }
  function bytesField(b) {
    return ethers.concat([uint32b(b.length), b]);
  }

  const memberChunks = [];
  for (let i = 0; i < NODES.length && i < signers.length; i++) {
    const node = NODES[i];
    const signer = signers[i];
    memberChunks.push(ethers.concat([
      addr32(signer.address),
      addr32(signer.address),
      addr32(signer.address),
      bytesField(ethers.toUtf8Bytes(node.name)),
      bytesField(ethers.getBytes("0x" + node.enode)),
      bytesField(ethers.toUtf8Bytes(node.ip)),
      uint32b(node.port),
    ]));
    log(`  Member ${i+1}: ${node.name} staker=${signer.address}`);
  }

  try {
    const tx = await govProxy.initOnce(registryAddr, LOCK_AMOUNT, ethers.concat(memberChunks));
    await tx.wait();
    log(`  Gov initialized with ${memberChunks.length} members`);
  } catch (e) {
    log(`  Gov.initOnce failed: ${e.message.slice(0, 120)}`);
  }

  // ── 12b. Initialize etcd cluster ────────────────────────────────────────────
  log("Initializing etcd cluster on node1...");
  try {
    const resp = await fetch(RPC_URL, {
      method: "POST",
      headers: {"Content-Type": "application/json"},
      body: JSON.stringify({jsonrpc:"2.0",method:"admin_etcdInit",params:[],id:1}),
    });
    const r = await resp.json();
    log(`  admin_etcdInit: ${JSON.stringify(r.result || r.error)}`);
  } catch(e) {
    log(`  admin_etcdInit failed: ${e.message.slice(0,80)}`);
  }

  // ── 12. Verify ───────────────────────────────────────────────────────────
  log("\n=== Verification ===");
  try {
    const magic = await registry.magic();
    log(`Registry magic: ${magic.toString(16)} (expected: 4d6574616469756d205265676973747279)`);
  } catch (e) {
    log(`Could not read magic: ${e.message}`);
  }

  try {
    const govAddrFromReg = await registry.getContractAddress(toBytes32("GovernanceContract"));
    log(`GovernanceContract in Registry: ${govAddrFromReg}`);
  } catch (e) {
    log(`Registry lookup failed: ${e.message}`);
  }

  try {
    const memberLen = await govProxy.getMemberLength();
    log(`Gov members: ${memberLen}`);
  } catch (e) {
    log(`getMemberLength failed: ${e.message}`);
  }

  // ── Output addresses ─────────────────────────────────────────────────────
  const result = {
    registry:     registryAddr,
    staking:      stakingAddr,
    ballot:       ballotAddr,
    envStorageImp: envImpAddr,
    envStorage:   envAddr,
    govImp:       govImpAddr,
    gov:          govAddr,
    deployer:     deployer.address,
    lockAmount:   LOCK_AMOUNT.toString(),
    blockReward:  BLOCK_REWARD.toString(),
    timestamp:    new Date().toISOString(),
  };

  const outFile = path.join(__dirname, "deployed-contracts.json");
  fs.writeFileSync(outFile, JSON.stringify(result, null, 2));
  log(`\nDeployed addresses saved to: ${outFile}`);
  log("\n=== Governance deployment complete ===");
  log("gmet nodes should detect Registry and start minting within ~1 block.");
  log(`Block reward: ${ethers.formatEther(BLOCK_REWARD)} META/block`);
  log(`Distribution: miners=${Number(DISTRIBUTION[0])/100}% staking=${Number(DISTRIBUTION[1])/100}% ecosystem=${Number(DISTRIBUTION[2])/100}% maintenance=${Number(DISTRIBUTION[3])/100}%`);
}

main().catch(e => {
  console.error("FATAL:", e.message);
  process.exit(1);
});
