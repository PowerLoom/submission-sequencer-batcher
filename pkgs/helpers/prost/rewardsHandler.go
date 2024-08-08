package prost

import (
	"collector/config"
	"collector/pkgs"
	"collector/pkgs/helpers/clients"
	"collector/pkgs/helpers/ipfs"
	"collector/pkgs/helpers/redis"
	"context"
	"fmt"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	log "github.com/sirupsen/logrus"
	"google.golang.org/protobuf/encoding/protojson"
	"math/big"
	"strconv"
	"sync"
	"time"
)

var (
	DailySnapshotQuota *big.Int
	RewardBasePoints   *big.Int
)

func CalculateAndStoreRewards(day, slotId *big.Int) {
	var slotRewardPoints *big.Int

	// Store rewards in redis: expiration = 1 day
	redis.Set(context.Background(), redis.SlotRewardsForDay(day.String(), slotId.String()), RewardBasePoints.String(), pkgs.Day*3)

	// Update total slot rewards
	// This is not necessary for daily rewards calculation
	if val, err := redis.Get(context.Background(), redis.TotalSlotRewards(slotId.String())); err != nil || val == "" {
		if slotRewardPoints, err = MustQuery[*big.Int](context.Background(), func() (*big.Int, error) {
			return Instance.SlotRewardPoints(&bind.CallOpts{}, config.SettingsObj.DataMarketContractAddress, slotId)
		}); err != nil {
			log.Errorf("Unable to query SlotRewardPoints for slot %s from contract: %s\n", slotId.String(), err.Error())
			redis.Delete(context.Background(), redis.TotalSlotRewards(slotId.String())) // Total rewards update failed, deleted the outdated value
			return
		}
	} else {
		slotRewardPoints, _ = new(big.Int).SetString(val, 10)
	}

	totalRewards := new(big.Int).Add(slotRewardPoints, RewardBasePoints)
	// Store total rewards as of this day
	redis.Set(context.Background(), redis.TotalSlotRewards(slotId.String()), totalRewards.String(), 0)
}

func UpdateRewards(day *big.Int) {
	slots := txManager.BatchUpdateRewards(day)
	txManager.EnsureRewardUpdateSuccess(day)
	if count, err := redis.Get(context.Background(), redis.TransactionReceiptCountByEvent(day.String())); err != nil && count != "" {
		log.Debugf("Transaction receipt fetches for day %s: %s", day.String(), count)
		n, _ := strconv.Atoi(count)
		if n > (len(slots)/config.SettingsObj.BatchSize)*3 { // giving upto 3 retries per txn
			clients.SendFailureNotification("EnsureRewardUpdateSuccess", fmt.Sprintf("Too many transaction receipts fetched for day %s: %s", day.String(), count), time.Now().String(), "Medium")
		}
	}
	redis.Delete(context.Background(), redis.TransactionReceiptCountByEvent(day.String()))

	var wg sync.WaitGroup
	maxConcurrency := 10
	semaphore := make(chan struct{}, maxConcurrency)

	for _, slot := range slots {
		wg.Add(1)
		semaphore <- struct{}{}
		go func(slot string) {
			defer wg.Done()
			defer func() { <-semaphore }()

			slotId, err := strconv.Atoi(slot)
			if err != nil {
				log.Errorln("Invalid slot ID: ", slot)
				return
			}
			clients.AssignSlotReward(slotId, int(day.Int64()))
		}(slot)
	}

	wg.Wait() // Wait for all goroutines to finish
}

func UpdateSubmissionCounts(batchSubmissions []*ipfs.BatchSubmission, day *big.Int) {
	for _, batchSubmission := range batchSubmissions {
		for _, sub := range batchSubmission.Batch.Submissions {
			submission := pkgs.SnapshotSubmission{}
			var ok bool

			newCount := big.NewInt(0)

			err := protojson.Unmarshal([]byte(sub), &submission)
			if err != nil {
				clients.SendFailureNotification("Submission unmarshalling", fmt.Sprintf("failed to unmarshal submission %s: %s", sub, err.Error()), time.Now().String(), "High")
				log.Errorln("failed to unmarshal submission: ", err)
				continue
			}
			slotId := new(big.Int).SetUint64(submission.Request.SlotId)
			count, err := redis.Get(context.Background(), redis.SlotSubmissionKey(slotId.String(), day.String()))
			if err == nil && count != "" {
				newCount, ok = new(big.Int).SetString(count, 10)
				if !ok {
					clients.SendFailureNotification("DailyTaskHandler", fmt.Sprintf("Incorrect bigInt stored in redis:  %s", count), time.Now().String(), "High")
					log.Errorln("Incorrect bigInt stored in redis: ", count)
					continue
				}
				newCount = new(big.Int).Add(newCount, big.NewInt(1))

				if newCount.Cmp(DailySnapshotQuota) == 0 {
					// Calculate and store rewards
					go CalculateAndStoreRewards(new(big.Int).Set(day), new(big.Int).Set(slotId))
				}
			} else {
				newCount = big.NewInt(1)
			}
			err = redis.AddToSet(context.Background(), redis.SlotSubmissionSetByDay(day.String()), redis.SlotSubmissionKey(slotId.String(), day.String()))
			if err != nil {
				log.Errorln("Error updating slot submission in redis: ", err)
			}

			err = redis.Set(context.Background(), redis.SlotSubmissionKey(slotId.String(), day.String()), newCount.String(), pkgs.Day*3)
			if err != nil {
				log.Errorln("Error updating slot submission in redis: ", err)
			}
		}
	}
}
