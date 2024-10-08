package config

import (
	"encoding/json"
	"github.com/ethereum/go-ethereum/common"
	log "github.com/sirupsen/logrus"
	"os"
	"strconv"
)

var SettingsObj *Settings

type Settings struct {
	ClientUrl                    string
	ContractAddress              string
	RedisHost                    string
	RedisPort                    string
	RedisDB                      string
	IPFSUrl                      string
	DataMarketAddress            string
	DataMarketContractAddress    common.Address
	SignerAccountAddresses       []string
	PrivateKeys                  []string
	AuthReadToken                string
	AuthWriteToken               string
	BatchSize                    int
	ChainID                      int64
	BlockTime                    int
	HttpTimeout                  int
	SlackReportingUrl            string
	RewardsBackendUrl            string
	PermissibleBatchesPerAccount int
}

func LoadConfig() {
	missingEnvVars := []string{}

	requiredEnvVars := []string{
		"PROST_RPC_URL",
		"PROTOCOL_STATE_CONTRACT",
		"REDIS_HOST",
		"REDIS_DB",
		"REDIS_PORT",
		"IPFS_URL",
		"DATA_MARKET_CONTRACT",
		"AUTH_READ_TOKEN",
		//"AUTH_WRITE_TOKEN",
		"SIGNER_ACCOUNT_ADDRESSES",
		"SIGNER_ACCOUNT_PRIVATE_KEYS",
		"BATCH_SIZE",
		"PROST_CHAIN_ID",
		"BLOCK_TIME",
		"SLACK_REPORTING_URL",
		"REWARDS_BACKEND_URL",
		"HTTP_TIMEOUT",
		"PERMISSIBLE_BATCHES_PER_ACCOUNT",
	}

	for envVar := range requiredEnvVars {
		if getEnv(requiredEnvVars[envVar], "") == "" {
			missingEnvVars = append(missingEnvVars, requiredEnvVars[envVar])
		}
	}

	if len(missingEnvVars) > 0 {
		log.Fatalf("Missing required environment variables: %v", missingEnvVars)
	}

	config := Settings{
		ClientUrl:                 getEnv("PROST_RPC_URL", ""),
		ContractAddress:           getEnv("PROTOCOL_STATE_CONTRACT", ""),
		RedisHost:                 getEnv("REDIS_HOST", ""),
		RedisPort:                 getEnv("REDIS_PORT", ""),
		RedisDB:                   getEnv("REDIS_DB", ""),
		IPFSUrl:                   getEnv("IPFS_URL", ""),
		DataMarketAddress:         getEnv("DATA_MARKET_CONTRACT", ""),
		AuthReadToken:             getEnv("AUTH_READ_TOKEN", ""),
		AuthWriteToken:            getEnv("AUTH_WRITE_TOKEN", ""),
		SlackReportingUrl:         getEnv("SLACK_REPORTING_URL", ""),
		RewardsBackendUrl:         getEnv("REWARDS_BACKEND_URL", ""),
		DataMarketContractAddress: common.HexToAddress(getEnv("DATA_MARKET_CONTRACT", "")),
	}

	signerAddressesList := []string{}
	signerAddressesListParseErr := json.Unmarshal(
		[]byte(getEnv("SIGNER_ACCOUNT_ADDRESSES", "[]")),
		&signerAddressesList,
	)
	if signerAddressesListParseErr != nil {
		log.Fatalf(
			"Failed to parse SIGNER_ACCOUNT_ADDRESSES environment variable: %v",
			signerAddressesListParseErr,
		)
	}
	config.SignerAccountAddresses = signerAddressesList

	signerPrivateKeysList := []string{}
	signerPrivateKeysListParseErr := json.Unmarshal(
		[]byte(getEnv("SIGNER_ACCOUNT_PRIVATE_KEYS", "[]")),
		&signerPrivateKeysList,
	)
	if signerPrivateKeysListParseErr != nil {
		log.Fatalf(
			"Failed to parse SIGNER_ACCOUNT_PRIVATE_KEYS environment variable: %v",
			signerPrivateKeysListParseErr,
		)
	}
	config.PrivateKeys = signerPrivateKeysList

	chainId, chainIdParseErr := strconv.ParseInt(getEnv("PROST_CHAIN_ID", ""), 10, 64)
	if chainIdParseErr != nil {
		log.Fatalf("Failed to parse PROST_CHAIN_ID environment variable: %v", chainIdParseErr)
	}
	config.ChainID = chainId

	batchSize, batchSizeParseErr := strconv.Atoi(getEnv("BATCH_SIZE", ""))
	if batchSizeParseErr != nil {
		log.Fatalf("Failed to parse BATCH_SIZE environment variable: %v", batchSizeParseErr)
	}
	config.BatchSize = batchSize

	permissibleBatches, permissibleBatchesParseErr := strconv.Atoi(getEnv("PERMISSIBLE_BATCHES_PER_ACCOUNT", ""))
	if permissibleBatchesParseErr != nil {
		log.Fatalf("Failed to parse PERMISSIBLE_BATCHES_PER_ACCOUNT environment variable: %v", permissibleBatchesParseErr)
	}
	config.PermissibleBatchesPerAccount = permissibleBatches

	blockTime, blockTimeParseErr := strconv.Atoi(getEnv("BLOCK_TIME", ""))
	if blockTimeParseErr != nil {
		log.Fatalf("Failed to parse BLOCK_TIME environment variable: %v", blockTimeParseErr)
	}
	config.BlockTime = blockTime

	httpTimeout, timeoutParseErr := strconv.Atoi(getEnv("HTTP_TIMEOUT", ""))
	if timeoutParseErr != nil {
		log.Fatalf("Failed to parse HTTP_TIMEOUT environment variable: %v", timeoutParseErr)
	}
	config.HttpTimeout = httpTimeout
	checkOptionalEnvVar(config.AuthWriteToken, "AUTH_WRITE_TOKEN")

	SettingsObj = &config
}

func getEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

func checkOptionalEnvVar(value, key string) {
	if value == "" {
		log.Warnf("Optional environment variable %s is not set", key)
	}
}
