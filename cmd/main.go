package main

import (
	"collector/config"
	"collector/pkgs/helpers/clients"
	"collector/pkgs/helpers/ipfs"
	"collector/pkgs/helpers/prost"
	"collector/pkgs/helpers/redis"
	"collector/pkgs/helpers/utils"
	"collector/pkgs/service"
	"sync"
	"time"
)

func main() {
	utils.InitLogger()
	config.LoadConfig()

	clients.InitializeReportingClient(config.SettingsObj.SlackReportingUrl, 5*time.Second)
	clients.InitializeRewardsBackendClient(config.SettingsObj.RewardsBackendUrl, 5*time.Second)

	var wg sync.WaitGroup

	prost.ConfigureClient()
	prost.ConfigureContractInstance()
	redis.RedisClient = redis.NewRedisClient()
	ipfs.ConnectIPFSNode()

	prost.PopulateStateVars()
	prost.InitializeTxManager()

	wg.Add(1)
	go service.StartApiServer()
	go prost.StartFetchingBlocks()
	wg.Wait()
}
