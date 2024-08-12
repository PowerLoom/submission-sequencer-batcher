package service

import (
	"collector/config"
	"collector/pkgs"
	"collector/pkgs/helpers/prost"
	"collector/pkgs/helpers/redis"
	_ "collector/pkgs/service/docs"
	"context"
	"encoding/json"
	"fmt"
	"github.com/cenkalti/backoff/v4"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	httpSwagger "github.com/swaggo/http-swagger"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// @title Swagger Example API
// @version 1.0
// @description This is an internal server used for data tracking and verification.
// @termsOfService http://swagger.io/terms/

// @contact.name API Support
// @contact.url http://www.swagger.io/support
// @contact.email support@swagger.io

var (
	rewardBasePoints   *big.Int
	dailySnapshotQuota *big.Int
	dataMarketAddress  common.Address
)

const baseExponent = int64(100000000)

type DailyRewardsRequest struct {
	SlotID int    `json:"slot_id"`
	Day    int    `json:"day"`
	Token  string `json:"token"`
}

type RewardsRequest struct {
	SlotID int    `json:"slot_id"`
	Token  string `json:"token"`
}

type SubmissionsRequest struct {
	SlotID   int    `json:"slot_id"`
	Token    string `json:"token"`
	PastDays int    `json:"past_days"`
}

type PastEpochsRequest struct {
	Token      string `json:"token"`
	PastEpochs int    `json:"past_epochs"`
}

type PastDaysRequest struct {
	Token    string `json:"token"`
	PastDays int    `json:"past_days"`
}

type PastBatchesRequest struct {
	Token       string `json:"token"`
	PastBatches int    `json:"past_batches"`
}

type DailySubmissions struct {
	Day         int   `json:"day"`
	Submissions int64 `json:"submissions"`
}

type LogType map[string]interface{}

type InfoType[K any] struct {
	Success  bool `json:"success"`
	Response K    `json:"response"`
}

type ResponseArray[K any] []K

type Response[K any] struct {
	Info      InfoType[K] `json:"info"`
	RequestID string      `json:"request_id"`
}

func getTotalRewards(slotId int) (int64, error) {
	ctx := context.Background()
	slotIdStr := strconv.Itoa(slotId)

	totalSlotRewardsKey := redis.TotalSlotRewards(slotIdStr)
	totalSlotRewards, err := redis.Get(ctx, totalSlotRewardsKey)

	if err != nil || totalSlotRewards == "" {
		slotIdBigInt := big.NewInt(int64(slotId))
		totalSlotRewardsBigInt, err := FetchSlotRewardsPoints(slotIdBigInt)
		if err != nil {
			return 0, err
		}

		totalSlotRewardsInt := totalSlotRewardsBigInt.Int64()

		var day *big.Int
		dayStr, _ := redis.Get(context.Background(), pkgs.SequencerDayKey)
		if dayStr == "" {
			//	TODO: Report unhandled error
			day, err = FetchDayCounter()
		} else {
			day, _ = new(big.Int).SetString(dayStr, 10)
		}

		currentDayRewardsKey := redis.SlotRewardsForDay(slotIdStr, day.String())
		currentDayRewards, err := redis.Get(ctx, currentDayRewardsKey)

		if err != nil || currentDayRewards == "" {
			currentDayRewards = "0"
		}

		currentDayRewardsInt, err := strconv.ParseInt(currentDayRewards, 10, 64)

		totalSlotRewardsInt += currentDayRewardsInt

		redis.Set(ctx, totalSlotRewardsKey, totalSlotRewards, 0)

		return totalSlotRewardsInt, nil
	}

	parsedRewards, err := strconv.ParseInt(totalSlotRewards, 10, 64)
	if err != nil {
		return 0, err
	}

	return parsedRewards, nil
}

func FetchSlotRewardsPoints(slotId *big.Int) (*big.Int, error) {
	var err error

	slotIdStr := slotId.String()
	var points *big.Int

	retryErr := backoff.Retry(func() error {
		points, err = prost.Instance.SlotRewardPoints(&bind.CallOpts{}, dataMarketAddress, slotId)
		return err
	}, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 5))

	if retryErr != nil {
		log.Errorf("Unable to query SlotRewardPoints for slot %s: %v", slotIdStr, retryErr)
		return nil, retryErr
	}

	return points, nil
}

func FetchDayCounter() (*big.Int, error) {
	var err error
	var dayCounter *big.Int

	retryErr := backoff.Retry(func() error {
		dayCounter, err = prost.Instance.DayCounter(&bind.CallOpts{}, dataMarketAddress)
		return err
	}, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 5))

	if retryErr != nil {
		return nil, fmt.Errorf("failed to fetch day counter: %v", retryErr)
	}

	return dayCounter, nil
}

func getDailySubmissions(slotId int, day *big.Int) int64 {
	if val, err := redis.Get(context.Background(), redis.SlotSubmissionKey(strconv.Itoa(slotId), day.String())); err != nil || val == "" {
		subs, err := prost.MustQuery[*big.Int](context.Background(), func() (*big.Int, error) {
			subs, err := prost.Instance.SlotSubmissionCount(&bind.CallOpts{}, dataMarketAddress, big.NewInt(int64(slotId)), day)
			return subs, err
		})
		if err != nil {
			log.Errorln("Could not fetch submissions from contract: ", err.Error())
			return 0
		}
		return subs.Int64()
	} else {
		submissions, _ := new(big.Int).SetString(val, 10)
		return submissions.Int64()
	}
}

// @Summary Get total submissions
// @Description Retrieves total submissions for a slot over a specified number of past days.
// @Tags Submissions
// @Accept json
// @Produce json
// @Param request body SubmissionsRequest true "Submissions Request"
// @Success 200 {object} Response[ResponseArray[DailySubmissions]] "Successful Response"
// @Failure 400 {string} string "Invalid request, past days less than 1, or invalid slotId"
// @Failure 401 {string} string "Unauthorized: Incorrect token"
// @Router /totalSubmissions [post]
func handleTotalSubmissions(w http.ResponseWriter, r *http.Request) {
	var request SubmissionsRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Authenticate token
	if request.Token != config.SettingsObj.AuthReadToken {
		http.Error(w, "Incorrect Token!", http.StatusUnauthorized)
		return
	}

	if request.PastDays < 1 {
		http.Error(w, "Past days should be at least 1", http.StatusBadRequest)
		return
	}

	slotID := request.SlotID
	if slotID < 1 || slotID > 10000 {
		http.Error(w, fmt.Sprintf("Invalid slotId: %d", slotID), http.StatusBadRequest)
		return
	}

	var day *big.Int
	dayStr, _ := redis.Get(context.Background(), pkgs.SequencerDayKey)
	if dayStr == "" {
		//	TODO: Report unhandled error
		day, _ = FetchDayCounter()
	} else {
		day, _ = new(big.Int).SetString(dayStr, 10)
	}

	currentDay := new(big.Int).Set(day)
	submissionsResponse := make([]DailySubmissions, request.PastDays)

	var wg sync.WaitGroup
	ch := make(chan DailySubmissions, request.PastDays)

	for i := 0; i < request.PastDays; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			day := new(big.Int).Sub(currentDay, big.NewInt(int64(i)))
			subs := getDailySubmissions(request.SlotID, day)
			ch <- DailySubmissions{Day: int(day.Int64()), Submissions: subs}
		}(i)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	for submission := range ch {
		submissionsResponse[int(currentDay.Int64())-submission.Day] = submission
	}

	info := InfoType[ResponseArray[DailySubmissions]]{
		Success:  true,
		Response: submissionsResponse,
	}

	response := Response[ResponseArray[DailySubmissions]]{
		Info:      info,
		RequestID: r.Context().Value("request_id").(string),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// @Summary Get finalized batch submissions
// @Description Retrieves logs of finalized batch submissions for past epochs.
// @Tags Epochs
// @Accept json
// @Produce json
// @Param request body PastEpochsRequest true "Past Epochs Request"
// @Success 200 {object} Response[ResponseArray[LogType]] "Successful Response"
// @Failure 400 {string} string "Invalid request or past epochs less than 0"
// @Failure 401 {string} string "Unauthorized: Incorrect token"
// @Router /finalizedBatchSubmissions [post]
func handleFinalizedBatchSubmissions(w http.ResponseWriter, r *http.Request) {
	var request PastEpochsRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Authenticate token
	if request.Token != config.SettingsObj.AuthReadToken {
		http.Error(w, "Incorrect Token!", http.StatusUnauthorized)
		return
	}

	pastEpochs := request.PastEpochs
	if pastEpochs < 0 {
		http.Error(w, "Past epochs should be at least 0", http.StatusBadRequest)
		return
	}

	keys := redis.RedisClient.Keys(context.Background(), fmt.Sprintf("%s.%s.*", pkgs.ProcessTriggerKey, pkgs.FinalizeBatches)).Val()

	sort.Strings(keys)
	var logs []LogType

	for _, key := range keys {
		entry, err := redis.Get(context.Background(), key)
		if err != nil {
			continue
		}

		var logEntry LogType
		err = json.Unmarshal([]byte(entry), &logEntry)
		if err != nil {
			continue
		}

		logs = append(logs, logEntry)
	}

	if pastEpochs > 0 && len(logs) > pastEpochs {
		logs = logs[len(logs)-pastEpochs:]
	}

	info := InfoType[ResponseArray[LogType]]{
		Success:  true,
		Response: logs,
	}
	response := Response[ResponseArray[LogType]]{
		Info:      info,
		RequestID: r.Context().Value("request_id").(string),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// @Summary Get triggered collection flows
// @Description Retrieves logs of triggered collection flows for past epochs.
// @Tags Epochs
// @Accept json
// @Produce json
// @Param request body PastEpochsRequest true "Past Epochs Request"
// @Success 200 {object} Response[ResponseArray[LogType]] "Successful Response"Response[ResponseArray[LogType]] "Successful Response"
// @Failure 400 {string} string "Invalid request or past epochs less than 0"
// @Failure 401 {string} string "Unauthorized: Incorrect token"
// @Router /triggeredCollectionFlows [post]
func handleTriggeredCollectionFlows(w http.ResponseWriter, r *http.Request) {
	var request PastEpochsRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Authenticate token
	if request.Token != config.SettingsObj.AuthReadToken {
		http.Error(w, "Incorrect Token!", http.StatusUnauthorized)
		return
	}

	pastEpochs := request.PastEpochs
	if pastEpochs < 0 {
		http.Error(w, "Past epochs should be at least 0", http.StatusBadRequest)
		return
	}

	keys := redis.RedisClient.Keys(context.Background(), fmt.Sprintf("%s.%s.*", pkgs.ProcessTriggerKey, pkgs.TriggerCollectionFlow)).Val()

	sort.Strings(keys)

	var logs []LogType

	for _, key := range keys {
		entry, err := redis.Get(context.Background(), key)
		if err != nil {
			continue
		}

		var logEntry LogType
		err = json.Unmarshal([]byte(entry), &logEntry)
		if err != nil {
			continue
		}

		logs = append(logs, logEntry)
	}

	if pastEpochs > 0 && len(logs) > pastEpochs {
		logs = logs[len(logs)-pastEpochs:]
	}

	info := InfoType[ResponseArray[LogType]]{
		Success:  true,
		Response: logs,
	}

	response := Response[ResponseArray[LogType]]{
		Info:      info,
		RequestID: r.Context().Value("request_id").(string),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// @Summary Get built batches
// @Description Retrieves logs of built batches for past batches.
// @Tags Batches
// @Accept json
// @Produce json
// @Param request body PastBatchesRequest true "Past Batches Request"
// @Success 200 {object} Response[ResponseArray[LogType]] "Successful Response"
// @Failure 400 {string} string "Invalid request or past batches less than 0"
// @Failure 401 {string} string "Unauthorized: Incorrect token"
// @Router /builtBatches [post]
func handleBuiltBatches(w http.ResponseWriter, r *http.Request) {
	var request PastBatchesRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Authenticate token
	if request.Token != config.SettingsObj.AuthReadToken {
		http.Error(w, "Incorrect Token!", http.StatusUnauthorized)
		return
	}

	pastBatches := request.PastBatches
	if pastBatches < 0 {
		http.Error(w, "Past batches should be at least 0", http.StatusBadRequest)
		return
	}

	keys := redis.RedisClient.Keys(context.Background(), fmt.Sprintf("%s.%s.*", pkgs.ProcessTriggerKey, pkgs.BuildBatch)).Val()

	sort.Strings(keys)

	var logs []LogType

	for _, key := range keys {
		entry, err := redis.Get(context.Background(), key)
		if err != nil {
			continue
		}

		var logEntry LogType
		err = json.Unmarshal([]byte(entry), &logEntry)
		if err != nil {
			continue
		}

		logs = append(logs, logEntry)
	}

	if pastBatches > 0 && len(logs) > pastBatches {
		logs = logs[len(logs)-pastBatches:]
	}

	info := InfoType[ResponseArray[LogType]]{
		Success:  true,
		Response: logs,
	}

	response := Response[ResponseArray[LogType]]{
		Info:      info,
		RequestID: r.Context().Value("request_id").(string),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// @Summary Get committed submission batches
// @Description Retrieves logs of committed submission batches for past batches.
// @Tags Batches
// @Accept json
// @Produce json
// @Param request body PastBatchesRequest true "Past Batches Request"
// @Success 200 {object} Response[ResponseArray[LogType]] "Successful Response"
// @Failure 400 {string} string "Invalid request or past batches less than 0"
// @Failure 401 {string} string "Unauthorized: Incorrect token"
// @Router /committedSubmissionBatches [post]
func handleCommittedSubmissionBatches(w http.ResponseWriter, r *http.Request) {
	var request PastBatchesRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Authenticate token
	if request.Token != config.SettingsObj.AuthReadToken {
		http.Error(w, "Incorrect Token!", http.StatusUnauthorized)
		return
	}

	pastBatches := request.PastBatches
	if pastBatches < 0 {
		http.Error(w, "Past batches should be at least 0", http.StatusBadRequest)
		return
	}

	keys := redis.RedisClient.Keys(context.Background(), fmt.Sprintf("%s.%s.*", pkgs.ProcessTriggerKey, pkgs.CommitSubmissionBatch)).Val()
	sort.Strings(keys)

	var logs []LogType

	for _, key := range keys {
		entry, err := redis.Get(context.Background(), key)
		if err != nil {
			continue
		}

		var logEntry LogType
		err = json.Unmarshal([]byte(entry), &logEntry)
		if err != nil {
			continue
		}

		logs = append(logs, logEntry)
	}

	if pastBatches > 0 && len(logs) > pastBatches {
		logs = logs[len(logs)-pastBatches:]
	}

	info := InfoType[ResponseArray[LogType]]{
		Success:  true,
		Response: logs,
	}
	response := Response[ResponseArray[LogType]]{
		Info:      info,
		RequestID: r.Context().Value("request_id").(string),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// @Summary Get batch resubmissions
// @Description Retrieves logs of batch resubmissions for past batches.
// @Tags Batches
// @Accept json
// @Produce json
// @Param request body PastBatchesRequest true "Past Batches Request"
// @Success 200 {object} Response[ResponseArray[LogType]] "Successful Response"
// @Failure 400 {string} string "Invalid request or past batches less than 0"
// @Failure 401 {string} string "Unauthorized: Incorrect token"
// @Router /batchResubmissions [post]
func handleBatchResubmissions(w http.ResponseWriter, r *http.Request) {
	var request PastBatchesRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Authenticate token
	if request.Token != config.SettingsObj.AuthReadToken {
		http.Error(w, "Incorrect Token!", http.StatusUnauthorized)
		return
	}

	pastBatches := request.PastBatches
	if pastBatches < 0 {
		http.Error(w, "Past batches should be at least 0", http.StatusBadRequest)
		return
	}

	keys := redis.RedisClient.Keys(context.Background(), fmt.Sprintf("%s.%s.*", pkgs.ProcessTriggerKey, pkgs.EnsureBatchSubmissionSuccess)).Val()

	var logs []LogType

	for _, key := range keys {
		entry, err := redis.Get(context.Background(), key)
		if err != nil {
			continue
		}

		var logEntry LogType
		err = json.Unmarshal([]byte(entry), &logEntry)
		if err != nil {
			continue
		}

		logs = append(logs, logEntry)
	}

	if pastBatches > 0 && len(logs) > pastBatches {
		logs = logs[len(logs)-pastBatches:]
	}

	info := InfoType[ResponseArray[LogType]]{
		Success:  true,
		Response: logs,
	}
	response := Response[ResponseArray[LogType]]{
		Info:      info,
		RequestID: r.Context().Value("request_id").(string),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// @Summary Get received epoch submissions count
// @Description Retrieves the total number of submissions received for past epochs.
// @Tags Submissions
// @Accept json
// @Produce json
// @Param request body PastEpochsRequest true "Past Epochs Request"
// @Success 200 {object} Response[int] "Successful Response"
// @Failure 400 {string} string "Invalid request or past epochs less than 0"
// @Failure 401 {string} string "Unauthorized: Incorrect token"
// @Router /receivedEpochSubmissionsCount [post]
func handleReceivedEpochSubmissionsCount(w http.ResponseWriter, r *http.Request) {
	var request PastEpochsRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Authenticate token
	if request.Token != config.SettingsObj.AuthReadToken {
		http.Error(w, "Incorrect Token!", http.StatusUnauthorized)
		return
	}

	pastEpochs := request.PastEpochs
	if pastEpochs < 0 {
		http.Error(w, "Past epochs should be at least 0", http.StatusBadRequest)
		return
	}

	keys := redis.RedisClient.Keys(context.Background(), fmt.Sprintf("%s.*", pkgs.EpochSubmissionsCountKey)).Val()
	sort.Strings(keys)

	var totalSubmissions int
	var epochs int
	end := len(keys) - 1

	if request.PastEpochs == 0 {
		epochs = len(keys)
	} else {
		epochs = request.PastEpochs
	}

	for i := 0; i < epochs; i++ {
		key := keys[end-i]
		entry, err := redis.Get(context.Background(), key)
		if err != nil {
			continue
		}

		if count, err := strconv.Atoi(entry); err == nil {
			totalSubmissions += count
		}
	}

	info := InfoType[int]{
		Success:  true,
		Response: totalSubmissions,
	}
	response := Response[int]{
		Info:      info,
		RequestID: r.Context().Value("request_id").(string),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// @Summary Get included epoch submissions count
// @Description Retrieves the total number of submissions included in batches for past epochs.
// @Tags Submissions
// @Accept json
// @Produce json
// @Param request body PastEpochsRequest true "Past Epochs Request"
// @Success 200 {object} Response[int] "Successful Response"
// @Failure 400 {string} string "Invalid request or past epochs less than 0"
// @Failure 401 {string} string "Unauthorized: Incorrect token"
// @Router /includedEpochSubmissionsCount [post]
func handleIncludedEpochSubmissionsCount(w http.ResponseWriter, r *http.Request) {
	var request PastEpochsRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Authenticate token
	if request.Token != config.SettingsObj.AuthReadToken {
		http.Error(w, "Incorrect Token!", http.StatusUnauthorized)
		return
	}

	pastEpochs := request.PastEpochs
	if pastEpochs < 0 {
		http.Error(w, "Past epochs should be at least 0", http.StatusBadRequest)
		return
	}

	keys := redis.RedisClient.Keys(context.Background(), fmt.Sprintf("%s.*", pkgs.BatchIncludedSubmissionsCount)).Val()
	sort.Strings(keys)

	var totalSubmissions int
	var epochs int

	end := len(keys) - 1

	if request.PastEpochs == 0 {
		epochs = len(keys)
	} else {
		epochs = request.PastEpochs
	}

	for i := 0; i < epochs; i++ {
		key := keys[end-i]
		entry, err := redis.Get(context.Background(), key)
		if err != nil {
			continue
		}

		if count, err := strconv.Atoi(entry); err == nil {
			totalSubmissions += count
		}
	}

	info := InfoType[int]{
		Success:  true,
		Response: totalSubmissions,
	}
	response := Response[int]{
		Info:      info,
		RequestID: r.Context().Value("request_id").(string),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// @Summary Get submissions for past epochs
// @Description Retrieves submission details for a specified number of past epochs.
// @Tags Submissions
// @Accept json
// @Produce json
// @Param request body PastEpochsRequest true "Past Epochs Request"
// @Success 200 {object} Response[ResponseArray[LogType]] "Successful Response"
// @Failure 400 {string} string "Invalid request or past epochs less than 0"
// @Failure 401 {string} string "Unauthorized: Incorrect token"
// @Router /receivedEpochSubmissions [post]
func handleReceivedEpochSubmissions(w http.ResponseWriter, r *http.Request) {
	var request PastEpochsRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Authenticate token
	if request.Token != config.SettingsObj.AuthReadToken {
		http.Error(w, "Incorrect Token!", http.StatusUnauthorized)
		return
	}

	pastEpochs := request.PastEpochs
	if pastEpochs < 0 {
		http.Error(w, "Past epochs should be at least 0", http.StatusBadRequest)
		return
	}

	// Fetch keys for epoch submissions
	keys := redis.RedisClient.Keys(context.Background(), fmt.Sprintf("%s.*", pkgs.EpochSubmissionsKey)).Val()
	sort.Strings(keys)

	var logs []LogType

	for _, key := range keys {
		epochId := strings.TrimPrefix(key, fmt.Sprintf("%s.", pkgs.EpochSubmissionsKey))
		epochLog := make(LogType)
		epochLog["epoch_id"] = epochId

		submissions := redis.RedisClient.HGetAll(context.Background(), key).Val()
		submissionsMap := make(LogType)
		for submissionId, submissionData := range submissions {
			var submission interface{}
			if err := json.Unmarshal([]byte(submissionData), &submission); err != nil {
				continue
			}
			submissionsMap[submissionId] = submission
		}
		epochLog["submissions"] = submissionsMap

		logs = append(logs, epochLog)
	}

	if pastEpochs > 0 && len(logs) > pastEpochs {
		logs = logs[len(logs)-pastEpochs:]
	}

	info := InfoType[ResponseArray[LogType]]{
		Success:  true,
		Response: logs,
	}
	response := Response[ResponseArray[LogType]]{
		Info:      info,
		RequestID: r.Context().Value("request_id").(string),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// FetchContractConstants fetches constants from the contract
func FetchContractConstants(ctx context.Context) error {
	var err error

	dataMarketAddress = common.HexToAddress(config.SettingsObj.DataMarketAddress)

	snapshotQuotaErr := backoff.Retry(func() error {
		dailySnapshotQuota, err = prost.Instance.DailySnapshotQuota(&bind.CallOpts{}, dataMarketAddress)
		return err
	}, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 5))

	if snapshotQuotaErr != nil {
		return fmt.Errorf("failed to fetch contract DailySnapshotQuota: %v", snapshotQuotaErr)
	}

	rewardsErr := backoff.Retry(func() error {
		rewardBasePoints, err = prost.Instance.RewardBasePoints(&bind.CallOpts{}, dataMarketAddress)
		return err
	}, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 5))

	if rewardsErr != nil {
		return fmt.Errorf("failed to fetch contract RewardBasePoints: %v", rewardsErr)
	}

	return nil
}

func FetchSlotSubmissionCount(slotID *big.Int, day *big.Int) (*big.Int, error) {
	var count *big.Int
	var err error

	retryErr := backoff.Retry(func() error {
		count, err = prost.Instance.SlotSubmissionCount(&bind.CallOpts{}, dataMarketAddress, slotID, day)
		return err
	}, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 5))

	if retryErr != nil {
		return nil, fmt.Errorf("failed to fetch submission count: %v", retryErr)
	}

	return count, nil
}

// @Summary Get daily rewards for a slot
// @Description Calculates the daily rewards for a given slot and day based on submission counts.
// @Tags Rewards
// @Accept json
// @Produce json
// @Param request body DailyRewardsRequest true "Daily Rewards Request"
// @Success 200 {object} Response[int] "Successful Response"
// @Failure 400 {string} string "Invalid request or slotId out of range"
// @Failure 401 {string} string "Unauthorized: Incorrect token"
// @Failure 500 {string} string "Internal Server Error"
// @Router /getDailyRewards [post]
func handleDailyRewards(w http.ResponseWriter, r *http.Request) {
	var dailySubmissionCount int64

	var request DailyRewardsRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Authenticate token
	if request.Token != config.SettingsObj.AuthReadToken {
		http.Error(w, "Incorrect Token!", http.StatusUnauthorized)
		return
	}

	slotID := request.SlotID
	if slotID < 1 || slotID > 10000 {
		http.Error(w, fmt.Sprintf("Invalid slotId: %d", slotID), http.StatusBadRequest)
		return
	}

	day := int64(request.Day)

	var rewards int
	key := redis.SlotSubmissionKey(strconv.Itoa(slotID), strconv.Itoa(request.Day))

	// Try to get from Redis
	dailySubmissionCountFromCache, err := redis.Get(r.Context(), key)

	if err != nil || dailySubmissionCountFromCache == "" {
		slotIDBigInt := big.NewInt(int64(slotID))
		count, err := FetchSlotSubmissionCount(slotIDBigInt, big.NewInt(day))
		if err != nil {
			http.Error(w, "Failed to fetch submission count: "+err.Error(), http.StatusInternalServerError)
			return
		}

		dailySubmissionCount = count.Int64()
		slotRewardsForDayKey := redis.SlotRewardsForDay(strconv.Itoa(slotID), strconv.Itoa(request.Day))
		redis.Set(r.Context(), slotRewardsForDayKey, strconv.FormatInt(dailySubmissionCount, 10), 0)
	} else if dailySubmissionCountFromCache != "" {
		dailyCount, err := strconv.ParseInt(dailySubmissionCountFromCache, 10, 64)
		if err != nil {
			log.Errorf("Failed to parse daily submission count from cache: %v", err)
		}
		dailySubmissionCount = dailyCount
	}

	log.Debugln("DailySnapshotQuota: ", dailySnapshotQuota)

	cmp := big.NewInt(dailySubmissionCount).Cmp(dailySnapshotQuota)
	if cmp >= 0 {
		rewards = int(new(big.Int).Div(rewardBasePoints, big.NewInt(baseExponent)).Int64())
	} else {
		rewards = 0
	}

	info := InfoType[int]{
		Success:  true,
		Response: rewards,
	}
	response := Response[int]{
		Info:      info,
		RequestID: r.Context().Value("request_id").(string),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// @Summary Get total rewards
// @Description Get the total rewards for a specific slot ID
// @Tags Rewards
// @Accept json
// @Produce json
// @Param request body RewardsRequest true "Rewards Request"
// @Success 200 {object} Response[int] "Successful Response"
// @Failure 400 {string} string "Invalid request or slotId out of range"
// @Failure 401 {string} string "Unauthorized: Incorrect token"
// @Failure 500 {string} string "Internal Server Error"
// @Router /getTotalRewards [post]
func handleTotalRewards(w http.ResponseWriter, r *http.Request) {
	var request RewardsRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Authenticate token
	if request.Token != config.SettingsObj.AuthReadToken {
		http.Error(w, "Incorrect Token!", http.StatusUnauthorized)
		return
	}

	slotID := request.SlotID
	if slotID < 1 || slotID > 10000 {
		http.Error(w, fmt.Sprintf("Invalid slotId: %d", slotID), http.StatusBadRequest)
		return
	}

	slotRewardPoints, err := getTotalRewards(slotID)
	if err != nil {
		http.Error(w, "Failed to fetch slot reward points: "+err.Error(), http.StatusInternalServerError)
		return
	}

	info := InfoType[int]{
		Success:  true,
		Response: int(slotRewardPoints),
	}
	response := Response[int]{
		Info:      info,
		RequestID: r.Context().Value("request_id").(string),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// @Summary Get reward updates
// @Description Retrieves logs of reward updates for a specific day.
// @Tags Rewards
// @Accept json
// @Produce json
// @Param request body PastDaysRequest true "Past Days Request"
// @Success 200 {object} Response[ResponseArray[LogType]] "Successful Response"
// @Failure 400 {string} string "Invalid request or day less than 0"
// @Failure 401 {string} string "Unauthorized: Incorrect token"
// @Router /rewardUpdates [post]
func handleRewardUpdates(w http.ResponseWriter, r *http.Request) {
	var request PastDaysRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Authenticate token
	if request.Token != config.SettingsObj.AuthReadToken {
		http.Error(w, "Incorrect Token!", http.StatusUnauthorized)
		return
	}

	pastDays := request.PastDays
	if pastDays < 0 {
		http.Error(w, "Past days should be at least 0", http.StatusBadRequest)
		return
	}

	// Fetch the current day from the Redis or any other source
	currentDayStr, _ := redis.Get(context.Background(), pkgs.SequencerDayKey)
	if currentDayStr == "" {
		// Handle error appropriately if the current day can't be fetched
		http.Error(w, "Failed to fetch current day", http.StatusInternalServerError)
		return
	}
	currentDay, err := strconv.Atoi(currentDayStr)
	if err != nil {
		http.Error(w, "Invalid current day format", http.StatusInternalServerError)
		return
	}

	// Calculate the range of days to fetch logs for
	startDay := currentDay - pastDays + 1

	// Fetch keys for reward updates logs
	keys := redis.RedisClient.Keys(context.Background(), fmt.Sprintf("%s.%s.*", pkgs.ProcessTriggerKey, pkgs.UpdateRewards)).Val()
	sort.Strings(keys)

	var logs []LogType

	// Filter keys that pertain to the specified range of past days
	for _, key := range keys {
		parts := strings.Split(key, ".")
		if len(parts) < 2 {
			continue
		}
		day, err := strconv.Atoi(parts[len(parts)-1])
		if err != nil || day < startDay || day > currentDay {
			continue
		}

		entry, err := redis.Get(context.Background(), key)
		if err != nil {
			continue
		}

		var logEntry LogType
		err = json.Unmarshal([]byte(entry), &logEntry)
		if err != nil {
			continue
		}

		logs = append(logs, logEntry)
	}

	info := InfoType[ResponseArray[LogType]]{
		Success:  true,
		Response: logs,
	}
	response := Response[ResponseArray[LogType]]{
		Info:      info,
		RequestID: r.Context().Value("request_id").(string),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func RequestMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := uuid.New().String()
		ctx := context.WithValue(r.Context(), "request_id", requestID)
		r = r.WithContext(ctx)

		log.WithField("request_id", requestID).Infof("Request started for: %s", r.URL.Path)

		w.Header().Set("X-Request-ID", requestID)

		next.ServeHTTP(w, r)

		log.WithField("request_id", requestID).Infof("Request ended")
	})
}

func StartApiServer() {
	err := FetchContractConstants(context.Background())
	if err != nil {
		log.Errorf("Failed to fetch contract constants: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/getTotalRewards", handleTotalRewards)
	mux.HandleFunc("/getDailyRewards", handleDailyRewards)
	mux.HandleFunc("/totalSubmissions", handleTotalSubmissions)
	mux.HandleFunc("/triggeredCollectionFlows", handleTriggeredCollectionFlows)
	mux.HandleFunc("/finalizedBatchSubmissions", handleFinalizedBatchSubmissions)
	mux.HandleFunc("/builtBatches", handleBuiltBatches)
	mux.HandleFunc("/committedSubmissionBatches", handleCommittedSubmissionBatches)
	mux.HandleFunc("/batchResubmissions", handleBatchResubmissions)
	mux.HandleFunc("/receivedEpochSubmissions", handleReceivedEpochSubmissions)
	mux.HandleFunc("/receivedEpochSubmissionsCount", handleReceivedEpochSubmissionsCount)
	mux.HandleFunc("/includedEpochSubmissionsCount", handleIncludedEpochSubmissionsCount)
	mux.HandleFunc("/rewardUpdates", handleRewardUpdates)

	handler := RequestMiddleware(mux)

	// Serve Swagger UI with the middleware
	swaggerHandler := httpSwagger.Handler(
		httpSwagger.URL("/swagger/doc.json"),
		httpSwagger.DeepLinking(true),
		httpSwagger.DocExpansion("none"),
		httpSwagger.DomID("swagger-ui"),
	)

	mux.Handle("/swagger/", RequestMiddleware(swaggerHandler))

	log.Println("Server is running on port 9988")
	log.Fatal(http.ListenAndServe(":9988", handler))
}
