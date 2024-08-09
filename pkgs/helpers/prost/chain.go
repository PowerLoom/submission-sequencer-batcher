package prost

import (
	"collector/config"
	"collector/pkgs"
	"collector/pkgs/contract"
	"collector/pkgs/helpers/redis"
	"context"
	"crypto/tls"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	log "github.com/sirupsen/logrus"
	"math/big"
	"net/http"
	"strings"
	"time"
)

var (
	Day            *big.Int
	Client         *ethclient.Client
	CurrentBlock   *types.Block
	CurrentEpochID = new(big.Int)
)

// TODO: Check ethclient options, connection issues
func ConfigureClient() {
	rpcClient, err := rpc.DialOptions(
		context.Background(),
		config.SettingsObj.ClientUrl,
		rpc.WithHTTPClient(
			&http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}},
		),
	)
	if err != nil {
		log.Errorf("Failed to connect to client: %s", err)
		log.Fatal(err)
	}
	Client = ethclient.NewClient(rpcClient)
}

func StartFetchingBlocks() {
	contractABI, err := abi.JSON(strings.NewReader(contract.ContractMetaData.ABI)) // Replace with your contract ABI

	if err != nil {
		log.Fatal(err)
	}

	for {
		var latestBlock *types.Block
		latestBlock, err = Client.BlockByNumber(context.Background(), nil) // Fetch the latest block available on the chain.
		if err != nil {
			log.Errorf("Failed to fetch latest block: %s", err)
			time.Sleep(100 * time.Millisecond) // Sleep briefly before retrying to prevent spamming.
			continue
		}

		// Check if there's a gap between the current block and the latest block on the chain.
		for blockNum := CurrentBlock.Number().Int64() + 1; blockNum <= latestBlock.Number().Int64(); blockNum++ {
			var block *types.Block
			block, err = Client.BlockByNumber(context.Background(), big.NewInt(blockNum))
			if err != nil {
				log.Errorf("Failed to fetch block %d: %s", blockNum, err)
				break // Break the inner loop to retry fetching this block on the next cycle.
			}

			if block == nil {
				log.Errorln("Received nil block for number: ", blockNum)
				break
			}

			CurrentBlock = block
			redis.Set(context.Background(), pkgs.SequencerCurrentBlockNumber, CurrentBlock.Number().String(), 0)

			log.Debugln("Processing block: ", CurrentBlock.Number().String())

			// Process events in the block.
			go ProcessEvents(block, contractABI)
		}
		// Sleep for approximately half the expected block time to balance load and responsiveness.
		time.Sleep(time.Duration(config.SettingsObj.BlockTime*500) * time.Millisecond)
	}
}
