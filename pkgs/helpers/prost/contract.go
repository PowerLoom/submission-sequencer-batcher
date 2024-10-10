package prost

import (
	"collector/config"
	"collector/pkgs"
	"collector/pkgs/contract"
	"collector/pkgs/helpers/clients"
	"collector/pkgs/helpers/redis"
	"context"
	"fmt"
	"github.com/cenkalti/backoff/v4"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	log "github.com/sirupsen/logrus"
	"math/big"
	"time"
)

var EpochsPerDay uint64

var Instance *contract.Contract

func ConfigureContractInstance() {
	Instance, _ = contract.NewContract(common.HexToAddress(config.SettingsObj.ContractAddress), Client)
}

func UpdateSubmissionLimit(curBlock *big.Int) *big.Int {
	var submissionLimit *big.Int
	if window, err := Instance.SnapshotSubmissionWindow(&bind.CallOpts{}, config.SettingsObj.DataMarketContractAddress); err != nil {
		clients.SendFailureNotification("Contract query error [UpdateSubmissionLimit]", fmt.Sprintf("Failed to fetch snapshot submission window: %s", err.Error()), time.Now().String(), "High")
		log.Errorf("Failed to fetch snapshot submission window: %s\n", err.Error())
	} else {
		submissionLimit = new(big.Int).Add(curBlock, window)
		submissionLimit = submissionLimit.Add(submissionLimit, big.NewInt(1))
		log.Debugln("Snapshot Submission Limit:", submissionLimit)
	}
	return submissionLimit
}

func MustQuery[K any](ctx context.Context, call func() (val K, err error)) (K, error) {
	expBackOff := backoff.NewConstantBackOff(1 * time.Second)

	var val K
	operation := func() error {
		var err error
		val, err = call()
		return err
	}
	// Use the retry package to execute the operation with backoff
	err := backoff.Retry(operation, backoff.WithMaxRetries(expBackOff, 3))
	if err != nil {
		clients.SendFailureNotification("Contract query error [MustQuery]", err.Error(), time.Now().String(), "High")
		return *new(K), err
	}
	return val, err
}

func PopulateStateVars() {
	for {
		if block, err := Client.BlockByNumber(context.Background(), nil); err == nil {
			CurrentBlock = block
			break
		} else {
			log.Debugln("Encountered error while fetching current block: ", err.Error())
		}
	}

	if output, err := Instance.CurrentEpoch(&bind.CallOpts{}, config.SettingsObj.DataMarketContractAddress); output.EpochId != nil && err == nil {
		CurrentEpochID.Set(output.EpochId)
		redis.Set(context.Background(), pkgs.CurrentEpoch, CurrentEpochID.String(), 0)
	} else {
		CurrentEpochID.Set(big.NewInt(0))
	}

	if output, err := MustQuery[*big.Int](context.Background(), func() (*big.Int, error) {
		return Instance.EpochsInADay(&bind.CallOpts{}, config.SettingsObj.DataMarketContractAddress)
	}); err == nil {
		EpochsPerDay = output.Uint64()
	} else {
		EpochsPerDay = pkgs.EpochsPerDay
	}

	if output, err := MustQuery[*big.Int](context.Background(), func() (*big.Int, error) {
		return Instance.DayCounter(&bind.CallOpts{}, config.SettingsObj.DataMarketContractAddress)
	}); err == nil {
		Day = output
	}

	DailySnapshotQuota, _ = MustQuery[*big.Int](context.Background(), func() (*big.Int, error) {
		return Instance.DailySnapshotQuota(&bind.CallOpts{}, config.SettingsObj.DataMarketContractAddress)
	})

	RewardBasePoints, _ = MustQuery[*big.Int](context.Background(), func() (*big.Int, error) {
		return Instance.RewardBasePoints(&bind.CallOpts{}, config.SettingsObj.DataMarketContractAddress)
	})
}
