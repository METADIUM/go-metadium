/* main.go */

package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/ethereum/go-ethereum/metadium/logrot"
)

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: logrot <file-name> <size> <count>\n")
}

func main() {
	if len(os.Args) != 4 {
		usage()
		os.Exit(0)
	}

	filename := os.Args[1]
	size, err1 := logrot.ParseSize(os.Args[2])
	count, err2 := strconv.Atoi(os.Args[3])
	if err1 != nil || err2 != nil || size <= 0 || count <= 0 {
		usage()
		os.Exit(1)
	}

	// Diagnostics go to stderr, which gmet.sh keeps in logs/logrot.err. They
	// used to go to whichever terminal started the node, which meant that when
	// rotation failed there was nothing left to explain it.
	if err := logrot.Run(os.Stdin, filename, size, count, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "logrot: %s\n", err)
		os.Exit(1)
	}
}

/* EOF */
