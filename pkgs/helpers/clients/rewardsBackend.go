package clients

import (
	"bytes"
	"collector/pkgs"
	"collector/pkgs/helpers/redis"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"github.com/cenkalti/backoff/v4"
	log "github.com/sirupsen/logrus"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

var rewardsBackendClient *RewardsBackend

type RewardsBackend struct {
	url    string
	client *http.Client
}

// UpdateSlotRewardMessage represents the structure of the request payload.
type UpdateSlotRewardMessage struct {
	Token      string `json:"token"`
	SlotID     int    `json:"slot_id"`
	DayCounter int    `json:"day_counter"`
}

func InitializeRewardsBackendClient(url string, timeout time.Duration) {
	rewardsBackendClient = &RewardsBackend{
		url:    url,
		client: &http.Client{Timeout: timeout, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}},
	}
	go PeriodicSlotRewardRetry()
}

func (rh *RewardsBackend) Do(req *http.Request) (*http.Response, error) {
	var resp *http.Response
	var err error
	err = backoff.Retry(func() error {
		resp, err = rh.client.Do(req)
		return err
	}, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 3))
	return resp, err
}

func BulkAssignSlotRewards(day int, slots []string) {
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
			if err = AssignSlotReward(slotId, day); err != nil {
				if err = redis.LockUpdateHashTable(pkgs.RewardsBackendFailures, strconv.Itoa(day), strconv.Itoa(slotId)); err != nil {
					SendFailureNotification("AssignSlotReward", fmt.Sprintf("Error updating failed request for slot %d: %s", slotId, err.Error()), time.Now().String(), "Medium")
					log.Errorf("Error updating failed request for slot %d: %s", slotId, err.Error())
				}
			}
		}(slot)
	}
	wg.Wait() // Wait for all goroutines to finish
}

func AssignSlotReward(slotId, day int) error {
	payload := UpdateSlotRewardMessage{
		Token:      "config.SettingsObj.AuthWriteToken",
		SlotID:     slotId,
		DayCounter: day,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		SendFailureNotification("AssignSlotReward", fmt.Sprintf("Error marshalling json: %s", err.Error()), time.Now().String(), "Medium")
		log.Errorln("Error marshalling JSON:", err)
		return err
	}

	req, err := http.NewRequest("POST", rewardsBackendClient.url+"/assignSlotReward", bytes.NewBuffer(jsonData))
	if err != nil {
		SendFailureNotification("AssignSlotReward", fmt.Sprintf("Error creating request: %s", err.Error()), time.Now().String(), "Medium")
		log.Errorln("Error creating request:", err)
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("accept", "application/json") // Ensure this matches your curl headers
	resp, err := rewardsBackendClient.Do(nil)
	if err != nil {
		SendFailureNotification("AssignSlotReward", fmt.Sprintf("Error sending request: %s", err.Error()), time.Now().String(), "Medium")
		log.Errorln("Error sending request:", err.Error())
		return err
	}
	defer resp.Body.Close()

	// Output the response status and body
	log.Debugln("Response status:", resp.Status)
	var responseBody map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&responseBody)
	log.Debugln("Response body:", responseBody)
	return nil
}

func PeriodicSlotRewardRetry() {
	for {
		// Fetch failed requests from Redis
		failedRequests := redis.RedisClient.HGetAll(context.Background(), pkgs.RewardsBackendFailures).Val()
		for dayStr, slotList := range failedRequests {
			day, err := strconv.Atoi(dayStr)
			if err != nil {
				// Not deleting the improper entry to maintain rewards state for slots
				log.Errorf("Incorrect day id %s stored in redis: %s", dayStr, err.Error())
				SendFailureNotification("PeriodicSlotRewardRetry", fmt.Sprintf("Incorrect day id %s stored in redis: %s", dayStr, err.Error()), time.Now().String(), "High")
			}
			slots := strings.Split(slotList, ",")
			// Remove all current entries - failed retries will end up in redis again
			if err = redis.RedisClient.HDel(context.Background(), pkgs.RewardsBackendFailures, dayStr).Err(); err != nil {
				log.Errorln("Redis failure: ", err.Error())
				SendFailureNotification("PeriodicSlotRewardRetry", fmt.Sprintf("Redis failure: ", err.Error()), time.Now().String(), "High")
				break
			}

			BulkAssignSlotRewards(day, slots)
		}
		time.Sleep(15 * time.Minute)
	}
}
