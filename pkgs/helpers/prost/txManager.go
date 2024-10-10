package prost

import (
	"collector/config"
	"collector/pkgs"
	"collector/pkgs/helpers/clients"
	"collector/pkgs/helpers/redis"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	log "github.com/sirupsen/logrus"
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
		if receiptString == "" {
			log.Errorf("Receipt not found in Redis for tx %s", txHash.Hex())
			return errors.New("receipt not found in Redis")
		}
		err = json.Unmarshal([]byte(receiptString), &receipt)
		if err != nil {
			clients.SendFailureNotification("GetTxReceipt", fmt.Sprintf("Failed to unmarshal txreceipt: %s", err.Error()), time.Now().String(), "Low")
			log.Errorf("Failed to unmarshal txreceipt: %s", err.Error())
			return err
		}
		log.Debugf("Fetched receipt for tx %s: %v", txHash.Hex(), receipt)
		redis.Delete(context.Background(), redis.ReceiptProcessed(txHash.Hex()))
		err = redis.RedisClient.Incr(context.Background(), redis.TransactionReceiptCountByEvent(identifier)).Err()
		if err != nil {
			clients.SendFailureNotification("GetTxReceipt", fmt.Sprintf("Failed to increment txreceipt count in Redis: %s", err.Error()), time.Now().String(), "Low")
			log.Errorf("Failed to increment txreceipt count in Redis: %s", err.Error())
		}
		return err
	}, backoff.WithMaxRetries(backoffLinear, 5))

	return receipt, err
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

func extractNonceFromError(err string) (uint64, bool) {
	regex := regexp.MustCompile(`nonce too low: next nonce (\d+), tx nonce \d+`)
	matches := regex.FindStringSubmatch(err)
	if len(matches) > 1 {
		nonce, err := strconv.ParseUint(matches[1], 10, 64)
		return nonce, err == nil
	}
	return 0, false
}
