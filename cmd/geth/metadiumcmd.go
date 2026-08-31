// metadiumcmd.go

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/cmd/utils"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metadium/logrot"
	"github.com/ethereum/go-ethereum/metadium/metclient"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/params"
	"github.com/urfave/cli/v2"
)

// gmet metadium new-account
var (
	metadiumCommand = &cli.Command{
		Name:      "metadium",
		Usage:     "Metadium helper commands",
		ArgsUsage: "",
		Category:  "METADIUM COMMANDS",
		Description: `

Metadium helper commands, create a new account, a new node id, a new genesis file, or a new admin contract file.`,
		Subcommands: []*cli.Command{
			{
				Name:   "new-account",
				Usage:  "Create a new account",
				Action: newAccount,
				Flags: []cli.Flag{
					utils.PasswordFileFlag,
					outFlag,
				},
				Description: `
    geth metadium new-account --out <file>

Creates a new account and saves it in the given file name.
To give password in command line, use "--password <(echo <password>)".
`,
			},
			{
				Name:   "new-nodekey",
				Usage:  "Create a new node key",
				Action: newNodeKey,
				Flags: []cli.Flag{
					outFlag,
				},
				Description: `
    geth metadium new-nodekey --out <file>

Creates a new node key and saves it in the given file name.
`,
			},
			{
				Name:   "nodeid",
				Usage:  "Print node id from node key",
				Action: nodeKey2Id,
				Description: `
    geth metadium new-nodekey <file>

Print node id from node key.
`,
			},
			{
				Name:      "genesis",
				Usage:     "Create a new genesis file",
				Action:    genGenesis,
				ArgsUsage: "<file-name>",
				Flags: []cli.Flag{
					dataFileFlag,
					genesisTemplateFlag,
					outFlag,
				},
				Description: `
    geth metadium genesis [--data <file> --genesis <file> --out <file>]

Generate a new genesis file from a template.

Stdin is used when --data is missing, and stdout is used for --out.

Data consists of "<account> <tokens>" or "<node id>".`,
			},
			{
				Name:   "admin-contract",
				Usage:  "Create an admin contract",
				Action: genAdminContract,
				Flags: []cli.Flag{
					dataFileFlag,
					adminTemplateFlag,
					outFlag,
				},
				Description: `
    geth metadium admin-contract [--data <file> --admin <file> --out <file>]

Generate a new admin contract file from a template.

Stdin is used when --data is missing, and stdout is used for --out.

Data consists of "<account> <balance> <tokens>" or "<node id>".
The first account becomes the coinbase of the genesis block, and the creator of the admin contract.
The first node becomes the boot miner who's allowed to generate blocks before admin contract gets created.`,
			},
			{
				Name:   "deploy-contract",
				Usage:  "Deploy a contract",
				Action: deployContract,
				Flags: []cli.Flag{
					utils.PasswordFileFlag,
					urlFlag,
					gasFlag,
					gasPriceFlag,
				},
				Description: `
    geth metadium deploy-contract [--password value --url <url> --gas <gas> --gasprice <gas-price>] <account-file> <contract-name> <contract-file.[js|json]>

Deploy a contract from a contract file in .js or .json format.`,
			},
			{
				Name:   "download-genesis",
				Usage:  "Download genesis file a peer",
				Action: downloadGenesis,
				Flags: []cli.Flag{
					urlFlag,
					outFlag,
				},
				Description: `
    geth metadium download-genesis [--url <url>] [--out <file-name>]

Download a genesis file from a peer to initialize.`,
			},
			{
				Name:   "deploy-governance",
				Usage:  "Deploy governance contracts",
				Action: deployGovernanceContracts,
				Flags: []cli.Flag{
					utils.PasswordFileFlag,
					urlFlag,
					gasFlag,
					gasPriceFlag,
				},
				Description: `
    geth metadium deploy-governance [--password value] [--url <url>] [--gas <gas>] [--gasprice <gas-price>] <contract-js-file> <config.js> <account-file>

Deploy governance contracts.
To give password in command line, use "--password <(echo <password>)".
`,
			},
		},
	}

	dataFileFlag = &cli.StringFlag{
		Name:  "data",
		Usage: "data file",
	}
	genesisTemplateFlag = &cli.StringFlag{
		Name:  "genesis",
		Usage: "genesis template file",
	}
	adminTemplateFlag = &cli.StringFlag{
		Name:  "admin",
		Usage: "admin contract template file",
	}
	outFlag = &cli.StringFlag{
		Name:  "out",
		Usage: "out file",
	}
	gasFlag = &cli.IntFlag{
		Name:  "gas",
		Usage: "gas amount",
	}
	gasPriceFlag = &cli.Int64Flag{
		Name:  "gasprice",
		Usage: "gas price", // in wei; exceeds a 32-bit int
	}
	urlFlag = &cli.StringFlag{
		Name:  "url",
		Usage: "url of gmet node",
	}
)

func newAccount(ctx *cli.Context) error {
	var err error

	w := os.Stdout
	if fn := ctx.String(outFlag.Name); fn != "" {
		w, err = os.OpenFile(fn, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if err != nil {
			utils.Fatalf("%v", err)
		}
	}

	password := utils.GetPassPhraseWithList("Please give a password. Do not forget this password.", true, 0, utils.MakePasswordList(ctx))

	key, err := keystore.NewKey(rand.Reader)
	if err != nil {
		return err
	}

	defer func() {
		b := key.PrivateKey.D.Bits()
		for i := range b {
			b[i] = 0
		}
	}()

	keyjson, err := keystore.EncryptKey(key, password, keystore.StandardScryptN, keystore.StandardScryptP)
	if err != nil {
		return err
	}

	_, err = w.Write(keyjson)
	return err
}

func newNodeKey(ctx *cli.Context) error {
	nodeKey, err := crypto.GenerateKey()
	if err != nil {
		return err
	}
	if err = crypto.SaveECDSA(ctx.String(outFlag.Name), nodeKey); err != nil {
		return err
	}
	return nil
}

func nodeKey2Id(ctx *cli.Context) error {
	if ctx.Args().Len() != 1 {
		utils.Fatalf("Nodekey file name is not given.")
	}
	nodeKey, err := crypto.LoadECDSA(ctx.Args().Get(0))
	if err != nil {
		return err
	}
	idv5 := fmt.Sprintf("%x", crypto.FromECDSAPub(&nodeKey.PublicKey)[1:])
	idv4 := enode.PubkeyToIDV4(&nodeKey.PublicKey)
	fmt.Printf("idv4: %v\nidv5: %v\n", idv4, idv5)
	// or
	//idv4 := enode.NewV4(&nodeKey.PublicKey, nil, 0, 0)
	//fmt.Printf("idv4: %v\idv5: %v\n", idv4.ID(), idv5)

	// to recover v4id from enode
	enodeUrl := fmt.Sprintf("enode://%v@127.0.0.1:8589", idv5)
	idv42, _ := enode.ParseV4(enodeUrl)
	_ = idv42

	return nil
}

type genesisConfig struct {
	ExtraData   string         `json:"extraData"`
	RewardPool  common.Address `json:"pool"`
	Maintenance common.Address `json:"maintenance"`
	Accounts    []*struct {
		Addr    common.Address `json:"addr"`
		Balance *big.Int       `json:"balance"`
	} `json:"accounts"`
	Members []*struct {
		Addr     common.Address `json:"addr"`
		Staker   common.Address `json:"staker"`
		Voter    common.Address `json:"voter"`
		Reward   common.Address `json:"reward"`
		Stake    *big.Int       `json:"stake"`
		Name     string         `json:"name"`
		Id       string         `json:"id"`
		Ip       string         `json:"ip"`
		Port     int            `json:"port"`
		Bootnode bool           `json:"bootnode"`
	} `json:"members"`
}

func loadGenesisConfig(r io.Reader) (*genesisConfig, error) {
	var config genesisConfig
	if data, err := io.ReadAll(r); err != nil {
		return nil, err
	} else if err = json.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	if len(config.Accounts) == 0 || len(config.Members) == 0 {
		return nil, fmt.Errorf("At least one account and node are required.")
	}

	bootnodeExists := false
	for _, m := range config.Members {
		// to conforming form to avoid checksum error
		if !(len(m.Id) == 128 || len(m.Id) == 130) {
			return nil, fmt.Errorf("Not a node id: %s\n", m.Id)
		}
		if len(m.Id) == 128 {
			m.Id = "0x" + m.Id
		}
		if m.Bootnode {
			bootnodeExists = true
			break
		}
	}

	if !bootnodeExists {
		return nil, fmt.Errorf("No bootnode found")
	}

	return &config, nil
}

func genGenesis(ctx *cli.Context) error {
	var err error

	var genesis map[string]interface{}
	if fn := ctx.String(genesisTemplateFlag.Name); fn == "" {
		utils.Fatalf("Genesis template is not specified.")
	} else if data, err := os.ReadFile(fn); err != nil {
		return err
	} else if err = json.Unmarshal(data, &genesis); err != nil {
		return err
	}

	r := os.Stdin
	if fn := ctx.String(dataFileFlag.Name); fn != "" {
		r, err = os.Open(fn)
		if err != nil {
			utils.Fatalf("%v", err)
		}
	}

	config, err := loadGenesisConfig(r)
	if err != nil {
		return err
	}

	w := os.Stdout
	if fn := ctx.String(outFlag.Name); fn != "" {
		w, err = os.OpenFile(fn, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if err != nil {
			utils.Fatalf("%v", err)
		}
	}

	if len(config.Members) <= 0 {
		utils.Fatalf("At least one member and node are required.")
	}

	bootacct, bootnode, emptyAddr := "", "", common.Address{}
	for _, i := range config.Members {
		if i.Bootnode {
			if !bytes.Equal(i.Addr[:], emptyAddr[:]) {
				bootacct = i.Addr.Hex()
			} else if !bytes.Equal(i.Staker[:], emptyAddr[:]) {
				bootacct = i.Staker.Hex()
			}
			bootnode = i.Id
			break
		}
	}

	genesis["coinbase"] = bootacct
	genesis["extraData"] = hexutil.Encode([]byte(fmt.Sprintf("%s\n%s", config.ExtraData, bootnode)))
	alloc := map[string]map[string]string{}
	for _, m := range config.Accounts {
		alloc[m.Addr.Hex()] = map[string]string{
			"balance": hexutil.EncodeBig(m.Balance),
		}
	}
	genesis["alloc"] = alloc

	x, err := json.MarshalIndent(genesis, "", "  ")
	if err != nil {
		return err
	}
	w.Write(x)
	return nil
}

func genAdminContract(ctx *cli.Context) error {
	var err error

	var f io.Reader
	if fn := ctx.String(adminTemplateFlag.Name); fn == "" {
		utils.Fatalf("Admin contract template is not specified.")
	} else {
		f, err = os.Open(fn)
		if err != nil {
			utils.Fatalf("%v", err)
		}
	}

	r := os.Stdin
	if fn := ctx.String(dataFileFlag.Name); fn != "" {
		r, err = os.Open(fn)
		if err != nil {
			utils.Fatalf("%v", err)
		}
	}

	config, err := loadGenesisConfig(r)
	if err != nil {
		return err
	}

	w := os.Stdout
	if fn := ctx.String(outFlag.Name); fn != "" {
		w, err = os.OpenFile(fn, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if err != nil {
			utils.Fatalf("%v", err)
		}
	}

	stakes := big.NewInt(0)
	for _, m := range config.Members {
		stakes.Add(stakes, m.Stake)
	}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		l := scanner.Text()
		if !strings.Contains(l, "// To Be Substituted") {
			_, err = fmt.Fprintln(w, l)
			if err != nil {
				return err
			}
			continue
		}

		ll := strings.TrimSpace(l)
		if strings.Index(ll, "tokens") == 0 {
			_, err = fmt.Fprintf(w, "        tokens = %d;\n", stakes)
		} else if strings.Index(ll, "address[") == 0 {
			var b bytes.Buffer
			b.WriteString(fmt.Sprintf("        address[%d] memory _members = [ ", len(config.Members)))
			first := true
			for _, m := range config.Members {
				if first {
					first = false
				} else {
					b.WriteString(", ")
				}
				b.WriteString(fmt.Sprintf("address(%s)", m.Addr))
			}
			b.Write([]byte(" ];\n"))
			_, err = b.WriteTo(w)
		} else if strings.Index(ll, "int[") == 0 {
			var b bytes.Buffer
			b.WriteString(fmt.Sprintf("        int[%d] memory _stakes = [ ", len(config.Members)))
			first := true
			for _, m := range config.Members {
				if first {
					first = false
				} else {
					b.WriteString(", ")
				}
				b.WriteString(fmt.Sprintf("int(%d)", m.Stake))
			}
			b.Write([]byte(" ];\n"))
			_, err = b.WriteTo(w)
		} else if strings.Index(ll, "Node[") == 0 {
			var b bytes.Buffer
			b.WriteString(fmt.Sprintf("        Node[%d] memory _nodes = [ ", len(config.Members)))
			first := true
			for _, n := range config.Members {
				if first {
					first = false
				} else {
					b.WriteString(", ")
				}
				b.WriteString(fmt.Sprintf(`Node(true, "%s", "%s", "%s", %d, 0, 0, "", "")`, n.Name, n.Id[2:], n.Ip, n.Port))
			}
			b.Write([]byte(" ];\n"))
			_, err = b.WriteTo(w)
		} else {
			_, err = fmt.Fprintln(w, l)
		}

		if err != nil {
			return err
		}
	}

	return nil
}

func deployContract(ctx *cli.Context) error {
	var err error

	passwd := ctx.String(utils.PasswordFileFlag.Name)
	url := ctx.String(urlFlag.Name)
	gas := ctx.Int(gasFlag.Name)
	gasPrice := ctx.Int64(gasPriceFlag.Name)

	if len(url) == 0 || ctx.Args().Len() != 3 {
		return fmt.Errorf("Invalid Arguments")
	}

	accountFile, contractName, contractFile := ctx.Args().Get(0), ctx.Args().Get(1), ctx.Args().Get(2)

	var acct *keystore.Key
	acct, err = metclient.LoadAccount(passwd, accountFile)
	if err != nil {
		return err
	}

	var contractData *metclient.ContractData
	contractData, err = metclient.LoadContract(contractFile, contractName)
	if err != nil {
		return err
	}

	ctxx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var cli *ethclient.Client
	cli, err = ethclient.Dial(url)
	if err != nil {
		return err
	}

	var hash common.Hash
	hash, err = metclient.Deploy(ctxx, cli, acct, contractData, nil, gas,
		gasPrice)
	if err != nil {
		return err
	}

	var receipt *types.Receipt
	receipt, err = metclient.GetContractReceipt(ctxx, cli, hash, 500, 60)
	if err != nil {
		return err
	} else {
		if receipt.Status == 1 {
			fmt.Printf("Contract mined! ")
		} else {
			fmt.Printf("Contract failed with %d! ", receipt.Status)
		}
		fmt.Printf("address: %s transactionHash: %s\n",
			receipt.ContractAddress.String(), hash.String())
	}

	return nil
}

type genesisReturn struct {
	Result string `json:"result"`
}

// canonicalGenesisConfig checks a genesis document served by a peer and, when it
// is one of the canonical Metadium networks, replaces its chain config with the
// compiled one.
//
// The genesis block hash does not cover the chain config, so a file that is
// stale by a fork -- one written before camelliaBlock existed, say -- produces
// the canonical hash while carrying a config that diverges from this binary's.
// Initializing an empty datadir with such a file runs the first session on that
// config, because $datadir/genesis.json is preferred over the embedded one
// (core.loadDefaultGenesisFile). A restart self-heals, since the stored-config
// path substitutes the compiled config, but the first session does not (#72).
//
// Everything except "config" is passed through byte for byte: the block fields
// are what the hash is computed over, and they matched. A genesis that is not a
// canonical network is left alone -- private networks bootstrap from a peer
// exactly this way, and there is nothing to compare them against.
//
// It returns the document to write, the network name ("" when not canonical),
// and whether the config was replaced.
func canonicalGenesisConfig(doc []byte) ([]byte, string, bool, error) {
	var parsed core.Genesis
	if err := json.Unmarshal(doc, &parsed); err != nil {
		return nil, "", false, fmt.Errorf("the served genesis does not parse: %w", err)
	}
	var (
		network  string
		compiled *params.ChainConfig
	)
	switch parsed.ToBlock().Hash() {
	case params.MetadiumMainnetGenesisHash:
		network, compiled = "mainnet", params.MetadiumMainnetChainConfig
	case params.MetadiumTestnetGenesisHash:
		network, compiled = "testnet", params.MetadiumTestnetChainConfig
	default:
		return doc, "", false, nil
	}
	served, err := json.Marshal(parsed.Config)
	if err != nil {
		return nil, network, false, err
	}
	want, err := json.Marshal(compiled)
	if err != nil {
		return nil, network, false, err
	}
	if bytes.Equal(served, want) {
		return doc, network, false, nil
	}
	// Replace the config key only, leaving the rest of the document untouched.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(doc, &raw); err != nil {
		return nil, network, false, err
	}
	raw["config"] = want
	out, err := json.MarshalIndent(raw, "", "    ")
	if err != nil {
		return nil, network, false, err
	}
	return append(out, '\n'), network, true, nil
}

func downloadGenesis(ctx *cli.Context) error {
	url := ctx.String(urlFlag.Name)
	if url == "" {
		return fmt.Errorf("URL is not given")
	}

	req := `{"id":1, "jsonrpc":"2.0", "method":"eth_genesis", "params":[]}`
	rsp, err := http.Post(url, "application/json", bytes.NewBuffer([]byte(req)))
	if err != nil {
		return err
	}

	buf := make([]byte, 1024*1024)
	n, err := rsp.Body.Read(buf)
	if err != nil && err != io.EOF {
		return err
	}

	var genesis genesisReturn
	if err := json.Unmarshal(buf[:n], &genesis); err != nil {
		return err
	}

	doc, network, replaced, err := canonicalGenesisConfig([]byte(genesis.Result))
	if err != nil {
		return err
	}
	switch {
	case network == "":
		log.Info("Genesis is not a canonical Metadium network, using it as served")
	case replaced:
		log.Warn("Peer served a stale chain config for a canonical network; using the compiled one",
			"network", network)
	default:
		log.Info("Genesis matches the canonical network and its compiled config", "network", network)
	}

	w := os.Stdout
	if fn := ctx.String(outFlag.Name); fn != "" {
		w, err = os.OpenFile(fn, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if err != nil {
			utils.Fatalf("%v", err)
		}
		defer w.Close()
	}

	w.Write(doc)
	return nil
}

// ignoreSIGPIPE keeps a closed log pipe from taking the node down.
//
// Go gives SIGPIPE on fd 1 and 2 the default disposition, so a node whose
// stdout is a pipe dies the moment the reader exits — no panic, no log line,
// nothing to find afterwards. Ignoring the signal turns those writes into
// ordinary EPIPE errors, which the logger discards, so the node keeps
// producing blocks while blind.
//
// It is installed only where the descriptors are expected to be pipes for the
// life of the process: the node action itself (gmet.sh pipes it into logrot)
// and the in-process --log path. One-shot commands keep the default so that
// `gmet dump ... | head` still terminates when its reader does.
func ignoreSIGPIPE() {
	signal.Ignore(unix.SIGPIPE)
}

// logrot frontend
func logrota(ctx *cli.Context) error {
	if !ctx.IsSet(utils.LogFlag.Name) {
		return nil
	}
	logflag := ctx.String(utils.LogFlag.Name)
	if logflag == "" {
		return nil
	}

	var err error
	logSize := int64(10 * 1024 * 1024)
	logCount := 5
	logOpts := strings.Split(logflag, ",")
	logFile := ""
	if len(logOpts) == 0 {
		return errors.New("No log file name")
	}
	if len(logOpts) >= 1 {
		logFile = strings.TrimSpace(logOpts[0])
	}
	if len(logOpts) >= 2 {
		if logSize, err = logrot.ParseSize(logOpts[1]); err != nil {
			return err
		}
		logCount = 1
	}
	if len(logOpts) >= 3 {
		// A count, not a size: "5g" here would quietly ask for five billion
		// generations, so no suffixes.
		if logCount, err = strconv.Atoi(strings.TrimSpace(logOpts[2])); err != nil {
			return fmt.Errorf("invalid log count %q: %v", logOpts[2], err)
		}
	}
	if logSize <= 0 || logCount <= 0 {
		return fmt.Errorf("log size and count must be positive, got %d and %d", logSize, logCount)
	}

	if dir := filepath.Dir(logFile); dir != "" && dir != "." {
		os.MkdirAll(filepath.Dir(logFile), 0700)
	}

	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	// Dup2 closes its target atomically; closing 1/2 first would open a
	// window in which any concurrent open() could claim the descriptor.
	unix.Dup2(int(w.Fd()), 1)
	unix.Dup2(int(w.Fd()), 2)

	// From here on the process's own stdout/stderr are backed by this pipe,
	// so a dead drainer must cost EPIPE, not the process.
	ignoreSIGPIPE()

	// Diagnostics cannot go to stderr here: stderr is the pipe this goroutine
	// is draining, so a broken log file would feed its own error reports back
	// into itself. They go beside the log instead.
	var diagw io.Writer = io.Discard
	if diag, derr := os.OpenFile(logFile+".err", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600); derr == nil {
		diagw = diag
	}

	go func() {
		// Run returns only once the pipe is closed or its parameters were
		// rejected — the latter cannot reach here, logSize/logCount are
		// validated above before the descriptors were touched. Whatever the
		// reason, leave it in the diagnostics rather than in the pipe.
		if err := logrot.Run(r, logFile, logSize, logCount, diagw); err != nil {
			fmt.Fprintf(diagw, "logrot: %s: rotation ended: %v (log output is being discarded)\n",
				time.Now().Format("2006-01-02T15:04:05Z0700"), err)
		}

		// Nothing else drains this pipe, so keep it drained regardless: a
		// full pipe would block the node forever on its next log write.
		io.Copy(io.Discard, r)
	}()

	return nil
}

// EOF
