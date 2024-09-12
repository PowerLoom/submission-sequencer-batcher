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
	"sync"
	"time"
)

var txManager *TxManager

const defaultGasLimit = uint64(20000000) // in units
var backoffInstance = backoff.NewExponentialBackOff()
var backoffLinear = backoff.NewConstantBackOff(1 * time.Second)

type TxManager struct {
	accountHandler *AccountHandler
}

func InitializeTxManager() {
	txManager = &TxManager{accountHandler: NewAccountHandler()}
	initializeBackoffInstance()
}

func initializeBackoffInstance() {
	backoffInstance.InitialInterval = 3 * time.Second
	backoffInstance.RandomizationFactor = 0.2
	backoffInstance.Multiplier = 1.2
	backoffInstance.MaxInterval = 30 * time.Second
	backoffInstance.MaxElapsedTime = 100 * time.Second
}

func (tm *TxManager) EndBatchSubmissionsForEpoch(epochId *big.Int) {
	account := tm.accountHandler.GetFreeAccount(true)
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
	if err = backoff.Retry(operation, backoff.WithMaxRetries(backoffInstance, 7)); err != nil {
		clients.SendFailureNotification("EndBatchSubmissionsForEpoch", fmt.Sprintf("Unable to EndBatchSubmissionsForEpoch %s: %s", epochId.String(), err.Error()), time.Now().String(), "High")
		log.Debugf("Batch submission completion signal for epoch %s failed after max retries: %s", epochId.String(), err.Error())
		return
	}
}

func (tm *TxManager) GetTxReceipt(txHash common.Hash, identifier string) (*types.Receipt, error) {
	var receipt *types.Receipt
	var err error
	time.Sleep(1 * time.Second) // waiting for few blocks to pass
	err = backoff.Retry(func() error {
		receiptString, err := redis.Get(context.Background(), redis.ReceiptProcessed(txHash.Hex()))
		err = json.Unmarshal([]byte(receiptString), &receipt)
		if err != nil && receiptString != "" {
			clients.SendFailureNotification("GetTxReceipt", fmt.Sprintf("Failed to unmarshal txreceipt: %s", err.Error()), time.Now().String(), "Low")
			log.Errorf("Failed to unmarshal txreceipt: %s", err.Error())
			return err
		}
		log.Debugf("Fetched receipt for tx %s: %v", txHash.Hex(), receipt)
		err = redis.RedisClient.Incr(context.Background(), redis.TransactionReceiptCountByEvent(identifier)).Err()
		if err != nil {
			clients.SendFailureNotification("GetTxReceipt", fmt.Sprintf("Failed to increment txreceipt count in Redis: %s", err.Error()), time.Now().String(), "Low")
			log.Errorf("Failed to increment txreceipt count in Redis: %s", err.Error())
		}
		return err
	}, backoff.WithMaxRetries(backoffLinear, 5))

	return receipt, err
}

func (tm *TxManager) CommitSubmissionBatches(batchSubmissions []*ipfs.BatchSubmission) {
	var wg sync.WaitGroup
	accounts := []*Account{}
	var requiredAccounts int

	batchDivision := len(batchSubmissions) / config.SettingsObj.PermissibleBatchesPerAccount
	if extra := len(batchSubmissions) % config.SettingsObj.PermissibleBatchesPerAccount; extra == 0 {
		requiredAccounts = batchDivision
	} else {
		requiredAccounts = batchDivision + 1
	}

	// try to get required free accounts
	for i := 0; i < requiredAccounts; i++ {
		if account := tm.accountHandler.GetFreeAccount(false); account != nil {
			accounts = append(accounts, account)
		}
	}

	if len(accounts) == 0 {
		log.Warnln("All accounts are occupied, waiting for one account and proceeding with batch submissions")
		accounts = append(accounts, tm.accountHandler.GetFreeAccount(true))
	}

	// divide evenly among available accounts
	batchesPerAccount := len(batchSubmissions) / len(accounts)
	var begin, end int
	// send asynchronous batch submissions
	for i := 0; i < len(accounts); i++ {
		account := accounts[i]

		begin = i * batchesPerAccount
		if i == len(accounts)-1 {
			end = len(batchSubmissions)
		} else {
			end = (i + 1) * batchesPerAccount
		}
		batch := batchSubmissions[begin:end]

		wg.Add(1)

		// Process the batch asynchronously
		go func(acc *Account, submissions []*ipfs.BatchSubmission) {
			defer wg.Done()

			// Commit each submission batch in the current batch
			for _, batchSubmission := range submissions {
				tm.CommitSubmissionBatch(acc, batchSubmission)
				acc.UpdateAuth(1)
			}

			// Release the account after processing the batch
			tm.accountHandler.ReleaseAccount(acc)
		}(account, batch)
	}

	// Wait for all goroutines to complete
	wg.Wait()
}

func (tm *TxManager) CommitSubmissionBatch(account *Account, batchSubmission *ipfs.BatchSubmission) {
	var tx *types.Transaction
	multiplier := 1
	nonce := account.auth.Nonce.String()
	var err error
	operation := func() error {
		tx, err = Instance.SubmitSubmissionBatch(account.auth, config.SettingsObj.DataMarketContractAddress, batchSubmission.Cid, batchSubmission.Batch.ID, batchSubmission.EpochId, batchSubmission.Batch.Pids, batchSubmission.Batch.Cids, [32]byte(batchSubmission.FinalizedCidsRootHash))
		if err != nil {
			multiplier = account.HandleTransactionError(err, multiplier, batchSubmission.Batch.ID.String())
			nonce = account.auth.Nonce.String()
			return err
		}
		return nil
	}
	if err = backoff.Retry(operation, backoff.WithMaxRetries(backoffInstance, 7)); err != nil {
		clients.SendFailureNotification("CommitSubmissionBatch", fmt.Sprintf("Batch %s submission for epoch %s failed after max retries: %s", batchSubmission.Batch.ID.String(), batchSubmission.EpochId.String(), err.Error()), time.Now().String(), "High")
		log.Debugf("Batch %s submission for epoch %s failed after max retries: ", batchSubmission.Batch.ID.String(), batchSubmission.EpochId.String())
		return
	}
	key := redis.BatchSubmissionKey(batchSubmission.Batch.ID.String(), nonce)
	batchSubmissionBytes, err := json.Marshal(batchSubmission)
	if err != nil {
		clients.SendFailureNotification("CommitSubmissionBatch", fmt.Sprintf("Unable to marshal ipfsBatchSubmission: %s", err.Error()), time.Now().String(), "High")
		log.Errorln("Unable to marshal ipfsBatchSubmission: ", err.Error())
	}
	value := fmt.Sprintf("%s.%s", tx.Hash().Hex(), string(batchSubmissionBytes))
	set := redis.BatchSubmissionSetByEpoch(batchSubmission.EpochId.String())
	err = redis.SetSubmission(context.Background(), key, value, set, 5*time.Minute)
	if err != nil {
		clients.SendFailureNotification("Redis error", err.Error(), time.Now().String(), "High")
		log.Debugln("Redis error: ", err.Error())
	}
	log.Debugf("Successfully submitted batch %s with nonce %s, gasPrice %s, tx: %s\n", batchSubmission.Batch.ID.String(), nonce, account.auth.GasPrice.String(), tx.Hash().Hex())
	logEntry := map[string]interface{}{
		"epoch_id":  batchSubmission.EpochId.String(),
		"batch_id":  batchSubmission.Batch.ID,
		"tx_hash":   tx.Hash().Hex(),
		"signer":    account.auth.From.Hex(),
		"nonce":     nonce,
		"timestamp": time.Now().Unix(),
	}
	if err = redis.SetProcessLog(context.Background(), redis.TriggeredProcessLog(pkgs.CommitSubmissionBatch, batchSubmission.Batch.ID.String()), logEntry, 4*time.Hour); err != nil {
		clients.SendFailureNotification("CommitSubmissionBatch", err.Error(), time.Now().String(), "High")
		log.Errorf("CommitSubmissionBatch process log error: %s ", err.Error())
	}
}

func (tm *TxManager) BatchUpdateRewards(day *big.Int) []string {
	slotIds := []*big.Int{}
	submissions := []*big.Int{}
	submittedSlots := []string{}

	var wg sync.WaitGroup
	accounts := []*Account{}

	// Fetch all valid slot-submission pairs from redis
	slotSubmissionCounts := redis.GetSetKeys(context.Background(), redis.SlotSubmissionSetByDay(day.String()))
	for _, key := range slotSubmissionCounts {
		val, err := redis.Get(context.Background(), key)
		if err != nil {
			clients.SendFailureNotification("BatchUpdateRewards", fmt.Sprintf("Reward updates redis error: %s", err.Error()), time.Now().String(), "Medium")
			log.Errorln("Reward updates redis error: ", err.Error())
			continue
		} else if val == "" {
			clients.SendFailureNotification("BatchUpdateRewards", fmt.Sprintf("Missing data entry in redis for key: %s", key), time.Now().String(), "Medium")
			log.Errorln("Missing data entry in redis for key: ", key)
			continue
		}
		log.Debugln("found kv pair: ", key, " : ", val)

		parts := strings.Split(key, ".")

		if len(parts) != 3 {
			clients.SendFailureNotification("BatchUpdateRewards", fmt.Sprintf("key does not have three parts: %s", key), time.Now().String(), "Medium")
			log.Errorln("key does not have 3 parts: ", key)
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

		slotIds = append(slotIds, slot)
		submissions = append(submissions, counts)
		submittedSlots = append(submittedSlots, slot.String())
	}

	slotsPerTransaction := config.SettingsObj.BatchSize

	// Send max slots = BatchSize per update transaction
	requiredTransactions := len(slotIds) / slotsPerTransaction

	var requiredAccounts int

	batchDivision := requiredTransactions / config.SettingsObj.PermissibleBatchesPerAccount
	if extra := requiredTransactions % config.SettingsObj.PermissibleBatchesPerAccount; extra == 0 {
		requiredAccounts = batchDivision
	} else {
		requiredAccounts = batchDivision + 1
	}

	// try to get required free accounts
	for i := 0; i < requiredAccounts; i++ {
		if account := tm.accountHandler.GetFreeAccount(false); account != nil {
			accounts = append(accounts, account)
		}
	}

	if len(accounts) == 0 {
		log.Warnln("All accounts are occupied, waiting for one account and proceeding with batch submissions")
		accounts = append(accounts, tm.accountHandler.GetFreeAccount(true))
	}

	// divide evenly among available accounts
	transactionsPerAccount := requiredTransactions / len(accounts)

	slotsPerAccount := transactionsPerAccount * slotsPerTransaction

	var begin, end int
	// send asynchronous batch submissions
	for i := 0; i < len(accounts); i++ {
		account := accounts[i]

		begin = i * slotsPerAccount
		if i == len(accounts)-1 {
			end = len(slotIds)
		} else {
			end = (i + 1) * slotsPerAccount
		}

		accountSlotIds := slotIds[begin:end]
		accountSubmissions := submissions[begin:end]

		wg.Add(1)

		// Process the slots asynchronously
		go func(acc *Account, slotIds, submissions []*big.Int, day *big.Int) {
			defer wg.Done()

			var start, finish int
			var steps int

			if len(slotIds)%slotsPerTransaction == 0 {
				steps = len(slotIds) / slotsPerTransaction
			} else {
				steps = len(slotIds)/slotsPerTransaction + 1
			}

			// Update slot rewards
			for i := 0; i < steps; i++ {
				start = i * slotsPerTransaction
				if i == steps-1 {
					finish = len(slotIds)
				} else {
					finish = (i + 1) * slotsPerTransaction
				}
				tm.UpdateRewards(acc, slotIds[start:finish], submissions[start:finish], day)
				acc.UpdateAuth(1)
			}

			// Release the account after processing the slots
			tm.accountHandler.ReleaseAccount(acc)
		}(account, accountSlotIds, accountSubmissions, day)
	}

	// Wait for all goroutines to complete
	wg.Wait()

	return submittedSlots
}

// TODO: Use multiple accounts
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
	if err = backoff.Retry(operation, backoff.WithMaxRetries(backoffInstance, 5)); err != nil {
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

	if err = redis.UpdateProcessLogTable(context.Background(), dayLogKey, tx.Hash().Hex(), logEntry); err != nil {
		clients.SendFailureNotification("UpdateRewards", err.Error(), time.Now().String(), "High")
		log.Errorf("UpdateRewards process log error: %s ", err.Error())
	}
}

func (tm *TxManager) EnsureRewardUpdateSuccess(day *big.Int) {
	account := tm.accountHandler.GetFreeAccount(true)
	defer tm.accountHandler.ReleaseAccount(account)
	resubmissionIterations := 0

	for {
		resubmissionIterations += 1
		hashes := redis.GetSetKeys(context.Background(), redis.RewardUpdateSetByDay(day.String()))
		if len(hashes) == 0 {
			return
		}
		if resubmissionIterations > pkgs.MaxRewardUpdateRetries {
			// Not removing these keys to keep reward state
			clients.SendFailureNotification("EnsureRewardUpdateSuccess", fmt.Sprintf("Reached max retry iterations for day: %s", day.String()), time.Now().String(), "High")
			log.Errorf("Reached max retry iterations for day: %s", day.String())
			return
		}
		for _, hash := range hashes {
			multiplier := 1
			if receipt, err := tm.GetTxReceipt(common.HexToHash(hash), day.String()); err != nil {
				if receipt != nil && receipt.Status == types.ReceiptStatusFailed {
					clients.SendFailureNotification("EnsureRewardUpdateSuccess", fmt.Sprintf("GetTxReceipt error for %s : %s", hash, err.Error()), time.Now().String(), "Low")
					log.Debugf("GetTxReceipt error for tx %s: %s", hash, err.Error())
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
				if err = backoff.Retry(operation, backoff.WithMaxRetries(backoffInstance, 5)); err != nil {
					clients.SendFailureNotification("EnsureRewardUpdateSuccess", fmt.Sprintf("Resubmitted reward updates for day %s slots %s failed: %s", day.String(), slotIdStrings, err.Error()), time.Now().String(), "High")
					log.Debugf("Resubmitted reward updates for day %s failed: ", day.String())
					return
				}

				redis.AddToSet(context.Background(), redis.RewardUpdateSetByDay(day.String()), reTx.Hash().Hex())
				redis.AddToSet(context.Background(), redis.RewardTxSlots(reTx.Hash().Hex()), slotIdStrings...)
				redis.AddToSet(context.Background(), redis.RewardTxSubmissions(reTx.Hash().Hex()), submissionStrings...)
			} else {
				if receipt != nil && receipt.Status == types.ReceiptStatusFailed {
					clients.SendFailureNotification("EnsureRewardUpdateSuccess", fmt.Sprintf("UpdateRewards execution failed: %s", hash), time.Now().String(), "Low")
					log.Debugln("Fetched receipt for unsuccessful tx: ", receipt.Logs)
				}
			}
			redis.RemoveFromSet(context.Background(), redis.RewardUpdateSetByDay(day.String()), hash)
			redis.Delete(context.Background(), redis.RewardTxSlots(hash))
			redis.Delete(context.Background(), redis.RewardTxSubmissions(hash))
		}
	}
}

func (tm *TxManager) EnsureBatchSubmissionSuccess(epochID *big.Int) {
	account := tm.accountHandler.GetFreeAccount(true)
	defer tm.accountHandler.ReleaseAccount(account)

	txSet := redis.BatchSubmissionSetByEpoch(epochID.String())
	resubmissionIterations := 0
	for {
		resubmissionIterations += 1
		keys := redis.RedisClient.SMembers(context.Background(), txSet).Val()
		if len(keys) == 0 {
			log.Debugln("No transactions remaining for epochID: ", epochID.String())
			if _, err := redis.RedisClient.Del(context.Background(), txSet).Result(); err != nil {
				log.Errorf("Unable to delete transaction from redis: %s\n", err.Error())
			}
			return
		}
		if resubmissionIterations > pkgs.MaxBatchRetries {
			clients.SendFailureNotification("EnsureBatchSubmissionSuccess", fmt.Sprintf("Reached max retry iterations for epoch: %s", epochID.String()), time.Now().String(), "High")
			log.Errorf("Reached max retry iterations for epoch: %s", epochID.String())
			for _, key := range keys {
				if _, err := redis.RedisClient.Del(context.Background(), key).Result(); err != nil {
					log.Errorf("Unable to delete transaction from redis: %s\n", err.Error())
				}
				if err := redis.RemoveFromSet(context.Background(), txSet, key); err != nil {
					log.Errorf("Unable to delete transaction from transaction set: %s\n", err.Error())
				}
			}
			if _, err := redis.RedisClient.Del(context.Background(), txSet).Result(); err != nil {
				log.Errorf("Unable to delete transaction from redis: %s\n", err.Error())
			}
			return
		}
		log.Debugf("Fetched %d transactions for epoch %d", len(keys), epochID)
		for _, key := range keys {
			if value, err := redis.Get(context.Background(), key); err != nil {
				clients.SendFailureNotification("EnsureBatchSubmissionSuccess", fmt.Sprintf("Unable to fetch value for key: %s\n", key), time.Now().String(), "High")
				log.Errorf("Unable to fetch value for key: %s\n", key)
				if _, err := redis.RedisClient.Del(context.Background(), key).Result(); err != nil {
					log.Errorf("Unable to delete transaction from redis: %s\n", err.Error())
				}
				if err = redis.RemoveFromSet(context.Background(), txSet, key); err != nil {
					log.Errorf("Unable to delete transaction from transaction set: %s\n", err.Error())
				}
				continue
			} else {
				vals := strings.Split(value, ".")
				if len(vals) < 2 {
					clients.SendFailureNotification("EnsureBatchSubmissionSuccess", fmt.Sprintf("txKey %s value in redis should have 2 parts: %s", key, value), time.Now().String(), "High")
					log.Errorf("txKey %s value in redis should have 2 parts: %s", key, value)
					if _, err := redis.RedisClient.Del(context.Background(), key).Result(); err != nil {
						log.Errorf("Unable to delete transaction from redis: %s\n", err.Error())
					}
					if err = redis.RemoveFromSet(context.Background(), txSet, key); err != nil {
						log.Errorf("Unable to delete transaction from transaction set: %s\n", err.Error())
					}
					continue
				}
				batchSubmission := &ipfs.BatchSubmission{}
				tx := vals[0]
				err = json.Unmarshal([]byte(vals[1]), batchSubmission)
				if err != nil {
					clients.SendFailureNotification("EnsureBatchSubmissionSuccess", fmt.Sprintf("Unable to unmarshal ipfsBatchSubmission %s: %s", vals[1], err.Error()), time.Now().String(), "High")
					log.Errorln("Unable to unmarshal ipfsBatchSubmission: ", err.Error())
					if _, err := redis.RedisClient.Del(context.Background(), key).Result(); err != nil {
						log.Errorf("Unable to delete transaction from redis: %s\n", err.Error())
					}
					if err = redis.RemoveFromSet(context.Background(), txSet, key); err != nil {
						log.Errorf("Unable to delete transaction from transaction set: %s\n", err.Error())
					}
					continue
				}
				nonce := strings.Split(key, ".")[1]
				multiplier := 1
				if receipt, err := tm.GetTxReceipt(common.HexToHash(tx), epochID.String()); err != nil {
					if receipt != nil {
						clients.SendFailureNotification("EnsureBatchSubmissionSuccess", fmt.Sprintf("GetTxReceipt error for %s : %s", tx, err.Error()), time.Now().String(), "Low")
						log.Debugf("GetTxReceipt error for tx %s: %s", tx, err.Error())
					}
					log.Errorf("Found unsuccessful transaction %s: %s, batchID: %d, nonce: %s", tx, err, batchSubmission.Batch.ID, nonce)
					updatedNonce := account.auth.Nonce.String()
					if err = account.UpdateGasPrice(context.Background(), multiplier); err != nil {
						log.Debugln("Unable to update gas price: ", err.Error())
					}
					var reTx *types.Transaction
					operation := func() error {
						reTx, err = Instance.SubmitSubmissionBatch(account.auth, config.SettingsObj.DataMarketContractAddress, batchSubmission.Cid, batchSubmission.Batch.ID, epochID, batchSubmission.Batch.Pids, batchSubmission.Batch.Cids, [32]byte(batchSubmission.FinalizedCidsRootHash))
						if err != nil {
							multiplier = account.HandleTransactionError(err, multiplier, batchSubmission.Batch.ID.String())
							updatedNonce = account.auth.Nonce.String()
							return err
						}
						return nil
					}
					if err = backoff.Retry(operation, backoff.WithMaxRetries(backoffInstance, 5)); err != nil {
						clients.SendFailureNotification("EnsureBatchSubmissionSuccess", fmt.Sprintf("Resubmission for batch %s failed: %s", batchSubmission.Batch.ID.String(), err.Error()), time.Now().String(), "High")
						log.Debugf("Resubmission for batch %s failed: ", batchSubmission.Batch.ID.String())
						return
					}
					txKey := redis.BatchSubmissionKey(batchSubmission.Batch.ID.String(), updatedNonce)
					batchSubmissionBytes, err := json.Marshal(batchSubmission)
					if err != nil {
						clients.SendFailureNotification("EnsureBatchSubmissionSuccess", fmt.Sprintf("Unable to marshal ipfsBatchSubmission: %s", err.Error()), time.Now().String(), "High")
					}
					txValue := fmt.Sprintf("%s.%s", reTx.Hash().Hex(), string(batchSubmissionBytes))
					if err = redis.SetSubmission(context.Background(), txKey, txValue, txSet, time.Hour); err != nil {
						clients.SendFailureNotification("Redis error", err.Error(), time.Now().String(), "High")
						log.Errorln("Redis ipfs error: ", err.Error())
						return
					}
					logEntry := map[string]interface{}{
						"epoch_id":  epochID.String(),
						"batch_id":  batchSubmission.Batch.ID.String(),
						"tx_hash":   reTx.Hash().Hex(),
						"signer":    account.auth.From.Hex(),
						"nonce":     updatedNonce,
						"timestamp": time.Now().Unix(),
					}

					existingLog, _ := redis.Get(context.Background(), redis.TriggeredProcessLog(pkgs.EnsureBatchSubmissionSuccess, batchSubmission.Batch.ID.String()))
					if existingLog == "" {
						redis.SetProcessLog(context.Background(), redis.TriggeredProcessLog(pkgs.EnsureBatchSubmissionSuccess, batchSubmission.Batch.ID.String()), logEntry, 4*time.Hour)
					} else { // append to existing log entry with another log entry
						existingEntries := make(map[string]interface{})
						err = json.Unmarshal([]byte(existingLog), &existingEntries)
						if err != nil {
							clients.SendFailureNotification("UpdateBatchResubmissionProcessLog", fmt.Sprintf("Unable to unmarshal log entry for resubmission of batch %s: %s", batchSubmission.Batch.ID.String(), err.Error()), time.Now().String(), "High")
							log.Errorln("Unable to unmarshal log entry for resubmission of batch: ", batchSubmission.Batch.ID.String())
							redis.SetProcessLog(context.Background(), redis.TriggeredProcessLog(pkgs.EnsureBatchSubmissionSuccess, batchSubmission.Batch.ID.String()), logEntry, 4*time.Hour)
						} else {
							utils.AppendToLogEntry(existingEntries, "tx_hash", reTx.Hash().Hex())
							utils.AppendToLogEntry(existingEntries, "nonce", updatedNonce)
							utils.AppendToLogEntry(existingEntries, "timestamp", time.Now().Unix())

							redis.SetProcessLog(context.Background(), redis.TriggeredProcessLog(pkgs.EnsureBatchSubmissionSuccess, batchSubmission.Batch.ID.String()), existingEntries, 4*time.Hour)
						}
					}
				} else {
					if receipt != nil && receipt.Status == types.ReceiptStatusFailed {
						clients.SendFailureNotification("EnsureBatchSubmissionSuccess", fmt.Sprintf(fmt.Sprintf("BatchSubmissionSuccess execution failed: %s", tx)), time.Now().String(), "Low")
						log.Debugln("Fetched receipt for unsuccessful tx: ", tx)
					}
				}
				if _, err := redis.RedisClient.Del(context.Background(), key).Result(); err != nil {
					log.Errorf("Unable to delete transaction from redis: %s\n", err.Error())
				}
				if err = redis.RemoveFromSet(context.Background(), txSet, key); err != nil {
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
