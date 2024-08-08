package prost

import (
	"collector/config"
	"collector/pkgs"
	"collector/pkgs/helpers/clients"
	"collector/pkgs/helpers/merkle"
	"collector/pkgs/helpers/redis"
	"context"
	"fmt"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/core/types"
	log "github.com/sirupsen/logrus"
	"math/big"
	"strconv"
	"time"
)

func ProcessEvents(block *types.Block, contractABI abi.ABI) {
	for _, tx := range block.Transactions() {
		receipt, err := Client.TransactionReceipt(context.Background(), tx.Hash())
		if err != nil {
			//log.Errorln(err.Error())
			continue
		}
		for _, vLog := range receipt.Logs {
			if vLog.Address.Hex() != config.SettingsObj.ContractAddress {
				continue
			}
			switch vLog.Topics[0].Hex() {
			case contractABI.Events["EpochReleased"].ID.Hex():
				event, err := Instance.ParseEpochReleased(*vLog)
				if err != nil {
					clients.SendFailureNotification("EpochRelease parse error", err.Error(), time.Now().String(), "High")
					log.Debugln("Error unpacking epochReleased event:", err)
					continue
				}
				if event.DataMarketAddress.Hex() == config.SettingsObj.DataMarketAddress {
					log.Debugf("Epoch Released at block %d: %s\n", block.Header().Number, event.EpochId.String())
					if CurrentEpochID.Cmp(event.EpochId) < 0 {
						CurrentEpochID = event.EpochId
						submissionLimit := UpdateSubmissionLimit(new(big.Int).Set(block.Number()))
						go processEpoch(event.EpochId, submissionLimit, block)
						redis.Set(context.Background(), pkgs.CurrentEpoch, CurrentEpochID.String(), 0)
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
	redis.SetProcessLog(context.Background(), redis.TriggeredProcessLog(pkgs.TriggerCollectionFlow, epochId.String()), logEntry, 4*time.Hour)

	updatedDay := new(big.Int).SetUint64(((epochId.Uint64() - 1) / EpochsPerDay) + 1 + pkgs.DayBuffer)
	if updatedDay.Cmp(Day) > 0 {
		prev := new(big.Int).Set(Day)
		Day = new(big.Int).Set(updatedDay)

		redis.Set(context.Background(), pkgs.SequencerDayKey, Day.String(), 0)
		triggerCollectionFlow(epochId, headers, prev)
		UpdateRewards(prev)
	} else {
		triggerCollectionFlow(epochId, headers, Day)
	}
}

func triggerCollectionFlow(epochID *big.Int, headers []string, day *big.Int) {
	LockDB = true

	if batchSubmissions, err := merkle.BuildBatchSubmissions(epochID, headers); err != nil {
		log.Debugln("Error building batched merkle tree: ", err)
		LockDB = false
	} else {
		txManager.CommitSubmissionBatches(batchSubmissions)
		//log.Debugf("Merkle tree built, resetting db for epoch: %d", epochID)
		// remove submissions as we no longer need them
		redis.ResetCollectorDBSubmissions(context.Background(), epochID, headers)
		LockDB = false
		// ensure all transactions were included after waiting for new block
		time.Sleep(time.Second * time.Duration(config.SettingsObj.BlockTime*len(batchSubmissions)))
		log.Debugln("Verifying all batch submissions")
		txManager.EnsureBatchSubmissionSuccess(epochID)
		if count, err := redis.Get(context.Background(), redis.TransactionReceiptCountByEvent(epochID.String())); err != nil && count != "" {
			log.Debugf("Transaction receipt fetches for epoch %s: %s", epochID.String(), count)
			n, _ := strconv.Atoi(count)
			if n > len(batchSubmissions)*3 { // giving upto 3 retries per txn
				clients.SendFailureNotification("EnsureBatchSubmissionSuccess", fmt.Sprintf("Too many transaction receipts fetched for epoch %s: %s", epochID.String(), count), time.Now().String(), "Medium")
			}
		}
		redis.Delete(context.Background(), redis.TransactionReceiptCountByEvent(epochID.String()))
		UpdateSubmissionCounts(batchSubmissions, day)
		txManager.EndBatchSubmissionsForEpoch(epochID)
	}
}
