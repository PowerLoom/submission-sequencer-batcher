package prost

import (
	"collector/config"
	"collector/pkgs"
	"collector/pkgs/helpers/clients"
	"collector/pkgs/helpers/ipfs"
	"collector/pkgs/helpers/redis"
	"context"
	"fmt"
	"math/big"
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	log "github.com/sirupsen/logrus"
	"google.golang.org/protobuf/encoding/protojson"
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
			clients.SendFailureNotification("CalculateAndStoreRewards", fmt.Sprintf("Unable to query SlotRewardPoints for slot %s from contract: %s\n", slotId.String(), err.Error()), time.Now().String(), "High")
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
	if count, err := redis.Get(context.Background(), redis.TransactionReceiptCountByEvent(day.String())); count != "" {
		log.Debugf("Transaction receipt fetches for day %s: %s", day.String(), count)
		n, _ := strconv.Atoi(count)
		if n > (len(slots)/config.SettingsObj.BatchSize)*3 { // giving up to 3 retries per txn
			clients.SendFailureNotification("EnsureRewardUpdateSuccess", fmt.Sprintf("Too many transaction receipts fetched for day %s: %s", day.String(), count), time.Now().String(), "Medium")
			log.Debugf("Too many transaction receipts fetched for day %s: %s", day.String(), count)
		}
	} else if err != nil {
		clients.SendFailureNotification("UpdateRewards transaction receipt fetch", fmt.Sprintf("Redis fetch error for day %s: %s", day.String(), err.Error()), time.Now().String(), "Medium")
		log.Debugf("UpdateRewards transaction receipt fetch error for day %s: %s", day.String(), err.Error())
	}
	redis.Delete(context.Background(), redis.TransactionReceiptCountByEvent(day.String()))

	clients.BulkAssignSlotRewards(int(day.Int64()), slots)
}

func UpdateSubmissionCounts(batchSubmissions []*ipfs.BatchSubmission, day *big.Int) {
	for _, batchSubmission := range batchSubmissions {
		for _, sub := range batchSubmission.Batch.Submissions {
			submission := pkgs.SnapshotSubmission{}
			err := protojson.Unmarshal([]byte(sub), &submission)
			if err != nil {
				clients.SendFailureNotification("Submission unmarshalling", fmt.Sprintf("failed to unmarshal submission %s: %s", sub, err.Error()), time.Now().String(), "High")
				log.Errorln("failed to unmarshal submission: ", err)
				continue
			}
			slotId := new(big.Int).SetUint64(submission.Request.SlotId)
			count, err := redis.Incr(context.Background(), redis.SlotSubmissionKey(slotId.String(), day.String()))
			countBigInt := big.NewInt(count)
			if err == nil && count != 0 {
				if countBigInt.Cmp(DailySnapshotQuota) == 0 {
					// Calculate and store rewards
					go CalculateAndStoreRewards(new(big.Int).Set(day), new(big.Int).Set(slotId))
				}
				err = redis.Expire(context.Background(), redis.SlotSubmissionKey(slotId.String(), day.String()), pkgs.Day*2)
				if err != nil {
					log.Errorln("Error setting expiry for slot submission in redis: ", err)
				}
				err = redis.AddToSet(context.Background(), redis.SlotSubmissionSetByDay(day.String()), redis.SlotSubmissionKey(slotId.String(), day.String()))
				if err != nil {
					clients.SendFailureNotification("UpdateSubmissionCounts", err.Error(), time.Now().String(), "High")
					log.Errorln("Error updating slot submission in redis: ", err)
				}
			} else {
				// send notification
				clients.SendFailureNotification("UpdateSubmissionCounts", fmt.Sprintf("Unable to increment submission count for slot %s on day %s: %s", slotId.String(), day.String(), err.Error()), time.Now().String(), "High")
				log.Errorln("Unable to increment submission count for slot: ", err)
			}
		}
	}
}
