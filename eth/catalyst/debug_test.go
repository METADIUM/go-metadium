package catalyst

import (
	"fmt"
	"testing"
)

func TestDebugWithdrawals(t *testing.T) {
	genesis, blocks := generateMergeChain(10, true)
	time := blocks[len(blocks)-1].Time() + 5
	genesis.Config.ShanghaiTime = &time
	
	for i, b := range blocks {
		fmt.Printf("Block %d: ExcessBlobGas=%v BlobGasUsed=%v Difficulty=%v\n",
			i, b.ExcessBlobGas(), b.BlobGasUsed(), b.Difficulty())
	}
	
	n, ethservice := startEthService(t, genesis, blocks)
	defer n.Close()
	_ = ethservice
}
