package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	DiscoveryTag      = "p2p-messenger-v1"
	defaultInputTitle = " Message — /help for commands "
	resetInputTitle   = " WARNING: /reset again to confirm, any command to cancel "
)

func loadBootstrapPeers(filename string) []string {
	peers := []string{
		"/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN",
		"/dnsaddr/bootstrap.libp2p.io/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXJJ16u19uLTa",
		"/dnsaddr/bootstrap.libp2p.io/p2p/QmbLHAnMoJPWSCR5Zhtx6BHJX9KiKNN6tpvbUcqanj75Nb",
		"/dnsaddr/bootstrap.libp2p.io/p2p/QmcZf59bWwK5XFi76CZX8cbJ4BhTzzA3gU1ZjYZcYW3dwt",
		"/ip4/104.131.131.82/tcp/4001/p2p/QmaCpDMGvV2BGHeYERUEnRQAwe3N8SzbUtfsmvsqQLuvuJ",
		"/ip4/104.236.179.241/tcp/4001/p2p/QmSoLPppuBtQSGwKDZT2M73ULpjvfd3aZ6ha4oFGL1KrGM",
	}
	file, err := os.Open(filename)

	if err != nil {
		fmt.Println("Create a file named bootstrap.txt containing a list of available servers")
		return peers
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if line != "" {
			peers = append(peers, line)
		}
	}

	if err := scanner.Err(); err != nil {
		log.Fatal(err)
	}

	return peers
}

var bootstrapPeers = loadBootstrapPeers("bootstrap.txt")
var debugMode bool

func debugLog(format string, args ...interface{}) {
	if debugMode {
		fmt.Printf(format+"\n", args...)
	}
}

func shortID(id peer.ID) string {
	value := id.String()
	if len(value) <= 12 {
		return value
	}

	return value[:6] + "..." + value[len(value)-6:]
}
