package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
)

// publicChainIDs are chain IDs a newly created private network must not reuse:
// transactions signed for one network would replay on the other, and PBFT
// commit seals are domain-separated by chain ID (docs/pbft-consensus-design.md
// §4.6, §9.5). Development defaults are listed because a production network
// that keeps one collides with every local test chain.
var publicChainIDs = map[uint64]string{
	1:        "Ethereum mainnet",
	5:        "Goerli",
	11:       "Metadium mainnet",
	12:       "Metadium testnet",
	1337:     "the local development default",
	17000:    "Holesky",
	31337:    "the Hardhat development default",
	11155111: "Sepolia",
}

var errZeroChainID = errors.New("chainId is 0: set a chain ID for this network " +
	"(see the numbering scheme in docs/pbft-consensus-design.md §9.5)")

// checkGenesisChainID reports a chain ID that must not be used (err) or should
// not be (warn). A missing chain ID is only a warning: upstream genesis files
// may omit it and fall back to a default.
func checkGenesisChainID(chainID *big.Int) (warn string, err error) {
	switch {
	case chainID == nil:
		return "genesis sets no chainId; a default applies, which other networks share", nil
	case chainID.Sign() <= 0:
		return "", errZeroChainID
	}
	if chainID.IsUint64() {
		if name, ok := publicChainIDs[chainID.Uint64()]; ok {
			return fmt.Sprintf("genesis chainId %v is %s; a new network must use its own "+
				"(docs/pbft-consensus-design.md §9.5) unless this initialises that network itself",
				chainID, name), nil
		}
	}
	return "", nil
}

// applyGenesisChainID sets the chain ID of a genesis template from the data
// file when the data file carries one, then checks the result. Unlike init,
// the generator refuses a missing chain ID: it only ever creates new
// networks, and the template ships a placeholder.
func applyGenesisChainID(genesis map[string]interface{}, chainID *big.Int) (warn string, err error) {
	config, ok := genesis["config"].(map[string]interface{})
	if !ok {
		return "", errors.New("genesis template has no config section")
	}
	if chainID != nil {
		config["chainId"] = chainID
	}
	var id *big.Int
	switch v := config["chainId"].(type) {
	case nil:
		return "", errors.New("chainId is not set: add \"chainId\" to the data file")
	case *big.Int:
		id = v
	default:
		// Numbers decoded from the template; re-encode to parse them exactly.
		raw, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		id = new(big.Int)
		if err := id.UnmarshalJSON(raw); err != nil {
			return "", fmt.Errorf("invalid chainId in genesis template: %s", raw)
		}
	}
	warn, err = checkGenesisChainID(id)
	if errors.Is(err, errZeroChainID) && chainID == nil {
		err = fmt.Errorf("%w; the template carries a placeholder, so add \"chainId\" to the data file", err)
	}
	return warn, err
}
