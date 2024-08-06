package prost

import (
	"collector/config"
	"collector/pkgs"
	"collector/pkgs/helpers/clients"
	"collector/pkgs/helpers/ipfs"
	"collector/pkgs/helpers/redis"
	"collector/pkgs/helpers/utils"
	"context"
	"encoding/json"
	"fmt"
	"github.com/cenkalti/backoff/v4"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	log "github.com/sirupsen/logrus"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var txManager *TxManager

const defaultGasLimit = uint64(20000000) // in units

type TxManager struct {
	accountHandler *AccountHandler
}

func InitializeTxManager() {
	txManager = &TxManager{accountHandler: NewAccountHandler()}
}

func (tm *TxManager) EndBatchSubmissionsForEpoch(epochId *big.Int) {
	account := tm.accountHandler.GetFreeAccount()
	defer tm.accountHandler.ReleaseAccount(account)
	multiplier := 1
	var err error
	operation := func() error {
		_, err = Instance.EndBatchSubmissions(account.auth, config.SettingsObj.DataMarketContractAddress, epochId)
		if err != nil {
			multiplier = account.HandleTransactionError(err, multiplier, epochId.String())
			return err
		}
		return nil
	}
	if err = backoff.Retry(operation, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 7)); err != nil {
		clients.SendFailureNotification("EndBatchSubmissionsForEpoch", fmt.Sprintf("Unable to EndBatchSubmissionsForEpoch %s: %s", epochId.String(), err.Error()), time.Now().String(), "High")
		log.Debugf("Batch submission completion signal for epoch %s failed after max retries: %s", epochId.String(), err.Error())
		return
	}
}

func (tm *TxManager) GetTxReceipt(txHash common.Hash) (*types.Receipt, error) {
	var receipt *types.Receipt
	var err error
	err = backoff.Retry(func() error {
		receipt, err = Client.TransactionReceipt(context.Background(), txHash)
		return err
	}, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 7))

	return receipt, err
}

func (tm *TxManager) CommitSubmissionBatches(batchSubmissions []*ipfs.BatchSubmission) {
	account := tm.accountHandler.GetFreeAccount()
	defer tm.accountHandler.ReleaseAccount(account)

	for _, batchSubmission := range batchSubmissions {
		tm.CommitSubmissionBatch(account, batchSubmission.Batch, batchSubmission.Cid, batchSubmission.EpochId, batchSubmission.FinalizedCidsRootHash)
		account.UpdateAuth(1)
	}
}

func (tm *TxManager) CommitSubmissionBatch(account *Account, batch *ipfs.Batch, cid string, epochId *big.Int, finalizedCidsRootHash []byte) {
	var tx *types.Transaction
	multiplier := 1
	nonce := account.auth.Nonce.String()
	var err error
	operation := func() error {
		tx, err = Instance.SubmitSubmissionBatch(account.auth, config.SettingsObj.DataMarketContractAddress, cid, batch.ID, epochId, batch.Pids, batch.Cids, [32]byte(finalizedCidsRootHash))
		if err != nil {
			multiplier = account.HandleTransactionError(err, multiplier, batch.ID.String())
			nonce = account.auth.Nonce.String()
			return err
		}
		return nil
	}
	if err = backoff.Retry(operation, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 7)); err != nil {
		clients.SendFailureNotification("CommitSubmissionBatch", fmt.Sprintf("Batch %s submission for epoch %s failed after max retries: %s", batch.ID.String(), epochId.String(), err.Error()), time.Now().String(), "High")
		log.Debugf("Batch %s submission for epoch %s failed after max retries: ", batch.ID.String(), epochId.String())
		return
	}
	key := redis.BatchSubmissionKey(batch.ID.String(), nonce)
	value := fmt.Sprintf("%s.%s.%d.%d.%s.%s.%s", tx.Hash().Hex(), cid, batch.ID, epochId, batch.Pids, batch.Cids, common.Bytes2Hex(finalizedCidsRootHash))
	set := redis.BatchSubmissionSetByEpoch(epochId.String())
	err = redis.SetSubmission(context.Background(), key, value, set, 5*time.Minute)
	if err != nil {
		log.Debugln("Redis error: ", err.Error())
	}
	log.Debugf("Successfully submitted batch %s with nonce %s, gasPrice %s, tx: %s\n", batch.ID.String(), nonce, account.auth.GasPrice.String(), tx.Hash().Hex())
	logEntry := map[string]interface{}{
		"epoch_id":  epochId.String(),
		"batch_id":  batch.ID,
		"tx_hash":   tx.Hash().Hex(),
		"signer":    account.auth.From.Hex(),
		"nonce":     nonce,
		"timestamp": time.Now().Unix(),
	}
	redis.SetProcessLog(context.Background(), redis.TriggeredProcessLog(pkgs.CommitSubmissionBatch, batch.ID.String()), logEntry, 4*time.Hour)
}

func (tm *TxManager) BatchUpdateRewards(day *big.Int) []string {
	slotIds := []*big.Int{}
	submissions := []*big.Int{}
	batchSize := config.SettingsObj.BatchSize
	submittedSlots := []string{}

	account := tm.accountHandler.GetFreeAccount()
	defer tm.accountHandler.ReleaseAccount(account)

	slotSubmissionCounts := redis.GetSetKeys(context.Background(), redis.SlotSubmissionSetByDay(day.String()))
	for _, key := range slotSubmissionCounts {
		val, err := redis.Get(context.Background(), key)
		if err != nil {
			log.Errorln("Reward updates redis error: ", err.Error())
			continue
		} else if val == "" {
			log.Errorln("Missing data entry in redis for key: ", key)
			continue
		}
		log.Debugln("found kv pair: ", key, " : ", val)

		parts := strings.Split(key, ".")

		if len(parts) != 3 {
			fmt.Println("key does not have three parts: ", key)
			continue
		}
		slot, ok := new(big.Int).SetString(parts[2], 10)
		if !ok {
			clients.SendFailureNotification("BatchUpdateRewards", fmt.Sprintf("Incorrect redis entry: %s", slot), time.Now().String(), "High")
			log.Errorf("Could not set slotId for key: %s, invalid value: %s", key, parts[2])
			continue
		}
		counts, ok := new(big.Int).SetString(val, 10)
		if !ok {
			clients.SendFailureNotification("BatchUpdateRewards", fmt.Sprintf("Incorrect redis entry: %s", counts), time.Now().String(), "High")
			log.Errorf("Could not set submissionCount for key: %s, invalid value: %s", key, val)
			continue
		}

		submittedSlots = append(submittedSlots, slot.String())
		slotIds = append(slotIds, slot)
		submissions = append(submissions, counts)
		if len(slotIds) == batchSize {
			txManager.UpdateRewards(account, slotIds, submissions, day)
			slotIds = []*big.Int{}
			submissions = []*big.Int{}
			account.UpdateAuth(1)
		}
	}

	if len(slotIds) > 0 {
		txManager.UpdateRewards(account, slotIds, submissions, day)
		account.UpdateAuth(1)
	}
	return submittedSlots
}

func (tm *TxManager) UpdateRewards(account *Account, slotIds, submissions []*big.Int, day *big.Int) {
	multiplier := 1
	var tx *types.Transaction
	var err error
	slotIdStrings := func() []string {
		slots := []string{}
		for _, slot := range slotIds {
			slots = append(slots, slot.String())
		}
		return slots
	}()
	submissionStrings := func() []string {
		subs := []string{}
		for _, sub := range submissions {
			subs = append(subs, sub.String())
		}
		return subs
	}()

	log.Debugln("Sending in slotIds: ", slotIdStrings)
	log.Debugln("Sending in submissions: ", submissionStrings)
	operation := func() error {
		tx, err = Instance.UpdateRewards(account.auth, config.SettingsObj.DataMarketContractAddress, slotIds, submissions, day)
		if err != nil {
			multiplier = account.HandleTransactionError(err, multiplier, fmt.Sprintf("day %s rewards", day.String()))
			return err
		}
		return nil
	}
	if err = backoff.Retry(operation, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 5)); err != nil {
		clients.SendFailureNotification("UpdateRewards", fmt.Sprintf("Reward updates for day %s failed: %s", day.String(), err.Error()), time.Now().String(), "High")
		log.Debugf("Reward updates for day %s failed: ", day.String())
		return
	}
	redis.AddToSet(context.Background(), redis.RewardUpdateSetByDay(day.String()), tx.Hash().Hex())
	redis.AddToSet(context.Background(), redis.RewardTxSlots(tx.Hash().Hex()), slotIdStrings...)
	redis.AddToSet(context.Background(), redis.RewardTxSubmissions(tx.Hash().Hex()), submissionStrings...)
	log.Debugf("Successfully updated batch rewards: %s", tx.Hash().Hex())

	// Store log entry for reward updates
	logEntry := map[string]interface{}{
		"slot_ids":          slotIds,
		"submission_counts": submissions,
		"signer":            account.auth.From.Hex(),
		"timestamp":         time.Now().Unix(),
	}

	dayLogKey := redis.TriggeredProcessLog(pkgs.UpdateRewards, day.String())
	existingLog, err := redis.Get(context.Background(), dayLogKey)
	logEntries := make(map[string]interface{})
	if err == nil && existingLog != "" {
		err = json.Unmarshal([]byte(existingLog), &logEntries)
		if err != nil {
			clients.SendFailureNotification("UpdateRewardsProcessLog", fmt.Sprintf("Error unmarshalling existing log entries: %v", err), time.Now().String(), "High")
			log.Errorf("Error unmarshalling existing log entries: %v", err)
		}
	}

	logEntries[tx.Hash().Hex()] = logEntry

	redis.SetProcessLog(context.Background(), redis.TriggeredProcessLog(pkgs.UpdateRewards, day.String()), logEntries, 4*time.Hour)
}

func (tm *TxManager) EnsureRewardUpdateSuccess(day *big.Int) {
	account := tm.accountHandler.GetFreeAccount()
	defer tm.accountHandler.ReleaseAccount(account)
	for {
		hashes := redis.GetSetKeys(context.Background(), redis.RewardUpdateSetByDay(day.String()))
		if len(hashes) == 0 {
			return
		}
		for _, hash := range hashes {
			multiplier := 1
			if receipt, err := tm.GetTxReceipt(common.HexToHash(hash)); err != nil {
				if receipt != nil {
					log.Debugln("Fetched receipt for unsuccessful tx: ", receipt.Logs)
				}
				log.Errorf("Found unsuccessful transaction %s, err: %s", hash, err.Error())
				slotIdStrings := redis.GetSetKeys(context.Background(), redis.RewardTxSlots(hash))
				submissionStrings := redis.GetSetKeys(context.Background(), redis.RewardTxSubmissions(hash))

				slotIds := func() []*big.Int {
					ids := []*big.Int{}
					for _, sId := range slotIdStrings {
						id, _ := new(big.Int).SetString(sId, 10)
						ids = append(ids, id)
					}
					return ids
				}()

				submissions := func() []*big.Int {
					subs := []*big.Int{}
					for _, sSub := range submissionStrings {
						sub, _ := new(big.Int).SetString(sSub, 10)
						subs = append(subs, sub)
					}
					return subs
				}()

				var reTx *types.Transaction
				operation := func() error {
					reTx, err = Instance.UpdateRewards(account.auth, config.SettingsObj.DataMarketContractAddress, slotIds, submissions, day)
					if err != nil {
						multiplier = account.HandleTransactionError(err, multiplier, fmt.Sprintf("day %s rewards", day.String()))
						return err
					}
					return nil
				}
				if err = backoff.Retry(operation, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 5)); err != nil {
					clients.SendFailureNotification("EnsureRewardUpdateSuccess", fmt.Sprintf("Resubmitted reward updates for day %s slots %s failed: %s", day.String(), slotIdStrings, err.Error()), time.Now().String(), "High")
					log.Debugf("Resubmitted reward updates for day %s failed: ", day.String())
					return
				}

				redis.AddToSet(context.Background(), redis.RewardUpdateSetByDay(day.String()), reTx.Hash().Hex())
				redis.AddToSet(context.Background(), redis.RewardTxSlots(reTx.Hash().Hex()), slotIdStrings...)
				redis.AddToSet(context.Background(), redis.RewardTxSubmissions(reTx.Hash().Hex()), submissionStrings...)
			}
			redis.RemoveFromSet(context.Background(), redis.RewardUpdateSetByDay(day.String()), hash)
			redis.Delete(context.Background(), redis.RewardTxSlots(hash))
			redis.Delete(context.Background(), redis.RewardTxSubmissions(hash))
		}
	}
}

// TODO: An endpoint is required for getting past batch submissions for 1 hour, do not delete all transactions
func (tm *TxManager) EnsureBatchSubmissionSuccess(epochID *big.Int) {
	account := tm.accountHandler.GetFreeAccount()
	defer tm.accountHandler.ReleaseAccount(account)

	txSet := redis.BatchSubmissionSetByEpoch(epochID.String())
	for {
		keys := redis.RedisClient.SMembers(context.Background(), txSet).Val()
		if len(keys) == 0 {
			log.Debugln("No transactions remaining for epochID: ", epochID.String())
			if _, err := redis.RedisClient.Del(context.Background(), txSet).Result(); err != nil {
				log.Errorf("Unable to delete transaction from redis: %s\n", err.Error())
			}
			return
		}
		log.Debugf("Fetched %d transactions for epoch %d", len(keys), epochID)
		for _, key := range keys {
			if value, err := redis.Get(context.Background(), key); err != nil {
				log.Errorf("Unable to fetch value for key: %s\n", key)
			} else {
				vals := strings.Split(value, ".")
				if len(vals) < 7 {
					clients.SendFailureNotification("EnsureBatchSubmissionSuccess", fmt.Sprintf("txKey %s value in redis should have 6 parts: %s", key, value), time.Now().String(), "High")
					log.Errorf("txKey %s value in redis should have 7 parts: %s", key, value)
					if _, err := redis.RedisClient.Del(context.Background(), key).Result(); err != nil {
						log.Errorf("Unable to delete transaction from redis: %s\n", err.Error())
					}
					if _, err := redis.RedisClient.SRem(context.Background(), txSet, key).Result(); err != nil {
						log.Errorf("Unable to delete transaction from transaction set: %s\n", err.Error())
					}
					continue
				}
				tx := vals[0]
				cid := vals[1]
				batchID := new(big.Int)
				_, ok := batchID.SetString(vals[2], 10)
				if !ok {
					log.Errorf("Unable to convert bigInt string to bigInt: %s\n", vals[2])
				}
				pids := strings.Fields(strings.Trim(vals[4], "[]"))
				cids := strings.Fields(strings.Trim(vals[5], "[]"))
				finalizedCidsRootHash := [32]byte(common.Hex2Bytes(vals[6]))
				nonce := strings.Split(key, ".")[1]
				multiplier := 1
				if receipt, err := tm.GetTxReceipt(common.HexToHash(tx)); err != nil {
					if receipt != nil {
						log.Debugln("Fetched receipt for unsuccessful tx: ", receipt.Logs)
					}
					log.Errorf("Found unsuccessful transaction %s: %s, batchID: %d, nonce: %s", err, tx, batchID, nonce)
					updatedNonce := account.auth.Nonce.String()
					if err = account.UpdateGasPrice(context.Background(), multiplier); err != nil {
						log.Debugln("Unable to update gas price: ", err.Error())
					}
					var reTx *types.Transaction
					operation := func() error {
						reTx, err = Instance.SubmitSubmissionBatch(account.auth, config.SettingsObj.DataMarketContractAddress, cid, batchID, epochID, pids, cids, finalizedCidsRootHash)
						if err != nil {
							multiplier = account.HandleTransactionError(err, multiplier, batchID.String())
							updatedNonce = account.auth.Nonce.String()
							return err
						}
						return nil
					}
					if err = backoff.Retry(operation, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 5)); err != nil {
						clients.SendFailureNotification("EnsureBatchSubmissionSuccess", fmt.Sprintf("Resubmission for batch %s failed: %s", batchID.String(), err.Error()), time.Now().String(), "High")
						log.Debugf("Resubmission for batch %s failed: ", batchID.String())
						return
					}
					txKey := redis.BatchSubmissionKey(batchID.String(), updatedNonce)
					txValue := fmt.Sprintf("%s.%s.%d.%d.%s.%s", reTx.Hash().Hex(), cid, batchID, epochID, pids, cids)
					if err = redis.SetSubmission(context.Background(), txKey, txValue, txSet, time.Hour); err != nil {
						log.Errorln("Redis ipfs error: ", err.Error())
						return
					}
					logEntry := map[string]interface{}{
						"epoch_id":  epochID.String(),
						"batch_id":  batchID,
						"tx_hash":   reTx.Hash().Hex(),
						"signer":    account.auth.From.Hex(),
						"nonce":     updatedNonce,
						"timestamp": time.Now().Unix(),
					}

					existingLog, _ := redis.Get(context.Background(), redis.TriggeredProcessLog(pkgs.EnsureBatchSubmissionSuccess, batchID.String()))
					if existingLog == "" {
						redis.SetProcessLog(context.Background(), redis.TriggeredProcessLog(pkgs.EnsureBatchSubmissionSuccess, batchID.String()), logEntry, 4*time.Hour)
					} // append to existing log entry with another log entry
					existingEntries := make(map[string]interface{})
					err = json.Unmarshal([]byte(existingLog), &existingEntries)
					if err != nil {
						clients.SendFailureNotification("UpdateBatchResubmissionProcessLog", fmt.Sprintf("Unable to unmarshal log entry for resubmission of batch %s: %s", batchID.String(), err.Error()), time.Now().String(), "High")
						log.Errorln("Unable to unmarshal log entry for resubmission of batch: ", batchID.String())
						redis.SetProcessLog(context.Background(), redis.TriggeredProcessLog(pkgs.EnsureBatchSubmissionSuccess, batchID.String()), logEntry, 4*time.Hour)
					} else {
						utils.AppendToLogEntry(existingEntries, "tx_hash", reTx.Hash().Hex())
						utils.AppendToLogEntry(existingEntries, "nonce", updatedNonce)
						utils.AppendToLogEntry(existingEntries, "timestamp", time.Now().Unix())

						redis.SetProcessLog(context.Background(), redis.TriggeredProcessLog(pkgs.EnsureBatchSubmissionSuccess, batchID.String()), existingEntries, 4*time.Hour)
					}
				}
				if _, err := redis.RedisClient.Del(context.Background(), key).Result(); err != nil {
					log.Errorf("Unable to delete transaction from redis: %s\n", err.Error())
				}
				if _, err := redis.RedisClient.SRem(context.Background(), txSet, key).Result(); err != nil {
					log.Errorf("Unable to delete transaction from transaction set: %s\n", err.Error())
				}
			}
		}
		time.Sleep(time.Second * time.Duration(config.SettingsObj.BlockTime*3))
	}
}

func extractNonceFromError(err string) (uint64, bool) {
	regex := regexp.MustCompile(`nonce too low: next nonce (\d+), tx nonce \d+`)
	matches := regex.FindStringSubmatch(err)
	if len(matches) > 1 {
		nonce, err := strconv.ParseUint(matches[1], 10, 64)
		return nonce, err == nil
	}
	return 0, false
}
