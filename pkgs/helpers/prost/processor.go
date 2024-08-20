package prost

import (
	"collector/config"
	"collector/pkgs"
	"collector/pkgs/helpers/clients"
	"collector/pkgs/helpers/merkle"
	"collector/pkgs/helpers/redis"
	"context"
	"fmt"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"math/big"
	"strconv"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/core/types"
	log "github.com/sirupsen/logrus"
)

func ProcessEvents(block *types.Block, contractABI abi.ABI) {
	var logs []types.Log
	var err error

	hash := block.Hash()
	filterQuery := ethereum.FilterQuery{
		BlockHash: &hash,
		Addresses: []common.Address{common.HexToAddress(config.SettingsObj.ContractAddress)},
	}

	operation := func() error {
		logs, err = Client.FilterLogs(context.Background(), filterQuery)
		return err
	}

	if err = backoff.Retry(operation, backoff.WithMaxRetries(backoff.NewConstantBackOff(200*time.Millisecond), 3)); err != nil {
		log.Errorln("Error fetching logs: ", err.Error())
		clients.SendFailureNotification("ProcessEvents", fmt.Sprintf("Error fetching logs: %s", err.Error()), time.Now().String(), "High")
		return
	}

	for _, vLog := range logs {
		switch vLog.Topics[0].Hex() {
		case contractABI.Events["EpochReleased"].ID.Hex():
			event, err := Instance.ParseEpochReleased(vLog)
			if err != nil {
				clients.SendFailureNotification("EpochRelease parse error", err.Error(), time.Now().String(), "High")
				log.Errorln("Error unpacking epochReleased event:", err)
				continue
			}
			if event.DataMarketAddress.Hex() == config.SettingsObj.DataMarketAddress {
				log.Debugf("Epoch Released at block %d: %s\n", block.Header().Number, event.EpochId.String())
				if CurrentEpochID.Cmp(event.EpochId) < 0 {
					CurrentEpochID = event.EpochId
					submissionLimit := UpdateSubmissionLimit(new(big.Int).Set(block.Number()))
					go processEpoch(event.EpochId, submissionLimit, block)
					if err = redis.Set(context.Background(), pkgs.CurrentEpoch, CurrentEpochID.String(), 0); err != nil {
						clients.SendFailureNotification("ProcessEvents", fmt.Sprintf("Unable to update current epoch in redis: %s", err.Error()), time.Now().String(), "High")
						log.Errorln("Unable to update current epoch in redis:", err.Error())
					}
				}
			}
		}
	}
}

func processEpoch(epochId, submissionLimit *big.Int, begin *types.Block) {
	cur := new(big.Int).Set(begin.Number())
	headers := []string{begin.Header().Hash().Hex()}
	for CurrentBlock.Number().Cmp(submissionLimit) < 0 {
		if cur.Cmp(CurrentBlock.Number()) < 0 {
			cur.Set(CurrentBlock.Number())
			header := CurrentBlock.Header().Hash().Hex()
			headers = append(headers, header)
			//log.Debugln("Adding header: ", header)
		}
		time.Sleep(time.Duration(config.SettingsObj.BlockTime*500) * time.Millisecond)
	}
	logEntry := map[string]interface{}{
		"epoch_id":      epochId.String(),
		"start_block":   begin.Number().String(),
		"current_block": cur.String(),
		"header_count":  len(headers),
		"timestamp":     time.Now().Unix(),
	}

	// Set timestamp and block number for triggered collection flow
	if err := redis.SetProcessLog(context.Background(), redis.TriggeredProcessLog(pkgs.TriggerCollectionFlow, epochId.String()), logEntry, 4*time.Hour); err != nil {
		clients.SendFailureNotification("TriggerCollectionFlow", err.Error(), time.Now().String(), "High")
		log.Errorln("TriggerCollectionFlow process log error: ", err.Error())
	}

	updatedDay := new(big.Int).SetUint64(((epochId.Uint64() - 1) / EpochsPerDay) + 1 + pkgs.DayBuffer)
	if updatedDay.Cmp(Day) > 0 {
		prev := new(big.Int).Set(Day)
		Day = new(big.Int).Set(updatedDay)

		err := redis.Set(context.Background(), pkgs.SequencerDayKey, Day.String(), 0)
		if err != nil {
			clients.SendFailureNotification("processEpoch", fmt.Sprintf("Unable to update day %s in redis: %s", Day.String(), err.Error()), time.Now().String(), "Medium")
			log.Errorf("Unable to update day %s in redis: %s", Day.String(), err.Error())
		}
		triggerCollectionFlow(epochId, headers, prev)
		UpdateRewards(prev)
		// set expiry of 24 hours for day submissions set and slot ID submissions by day keys within that set
		prevDaySlotSubmissionsKeySet := redis.SlotSubmissionSetByDay(prev.String())
		err = redis.Expire(context.Background(), prevDaySlotSubmissionsKeySet, pkgs.Day*7)
		if err != nil {
			clients.SendFailureNotification("processEpoch", fmt.Sprintf("Unable to set expiry for %s in redis: %s", prevDaySlotSubmissionsKeySet, err.Error()), time.Now().String(), "Medium")
			log.Errorf("Unable to set expiry for %s in redis: %s", prevDaySlotSubmissionsKeySet, err.Error())
		}
	} else {
		triggerCollectionFlow(epochId, headers, Day)
	}
}

func triggerCollectionFlow(epochID *big.Int, headers []string, day *big.Int) {

	if batchSubmissions, err := merkle.BuildBatchSubmissions(epochID, headers); err != nil {
		log.Debugln("Error building batched merkle tree: ", err)
	} else {
		txManager.CommitSubmissionBatches(batchSubmissions)
		//log.Debugf("Merkle tree built, resetting db for epoch: %d", epochID)
		// remove submissions as we no longer need them
		redis.ResetCollectorDBSubmissions(context.Background(), epochID, headers)
		// ensure all transactions were included after waiting for new block
		log.Debugln("Verifying all batch submissions")
		txManager.EnsureBatchSubmissionSuccess(epochID)
		if count, err := redis.Get(context.Background(), redis.TransactionReceiptCountByEvent(epochID.String())); count != "" {
			log.Debugf("Transaction receipt fetches for epoch %s: %s", epochID.String(), count)
			n, _ := strconv.Atoi(count)
			if n > len(batchSubmissions)*3 { // giving upto 3 retries per txn
				clients.SendFailureNotification("EnsureBatchSubmissionSuccess", fmt.Sprintf("Too many transaction receipts fetched for epoch %s: %s", epochID.String(), count), time.Now().String(), "Medium")
				log.Errorf("Too many transaction receipts fetched for epoch %s: %s", epochID.String(), count)
			}
		} else if err != nil {
			clients.SendFailureNotification("Redis error", err.Error(), time.Now().String(), "High")
			log.Errorln("Redis error: ", err.Error())
		}
		redis.Delete(context.Background(), redis.TransactionReceiptCountByEvent(epochID.String()))
		UpdateSubmissionCounts(batchSubmissions, day)
		txManager.EndBatchSubmissionsForEpoch(epochID)
	}
}
