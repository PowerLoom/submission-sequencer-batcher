package clients

import (
	"bytes"
	"collector/config"
	"collector/pkgs"
	"collector/pkgs/helpers/redis"
	"crypto/tls"
	"encoding/json"
	"fmt"
	log "github.com/sirupsen/logrus"
	"net/http"
	"strconv"
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
}

func (rh *RewardsBackend) Do(req *http.Request) (*http.Response, error) {
	return rh.client.Do(req)
}

func AssignSlotReward(slotId, day int) {
	payload := UpdateSlotRewardMessage{
		Token:      config.SettingsObj.AuthWriteToken,
		SlotID:     slotId,
		DayCounter: day,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		SendFailureNotification("AssignSlotReward", fmt.Sprintf("Error marshalling json: %s", err.Error()), time.Now().String(), "Medium")
		log.Errorln("Error marshalling JSON:", err)
		return
	}

	req, err := http.NewRequest("POST", rewardsBackendClient.url+"/assignSlotReward", bytes.NewBuffer(jsonData))
	if err != nil {
		SendFailureNotification("AssignSlotReward", fmt.Sprintf("Error creating request: %s", err.Error()), time.Now().String(), "Medium")
		log.Errorln("Error creating request:", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("accept", "application/json") // Ensure this matches your curl headers

	resp, err := rewardsBackendClient.Do(req)
	if err != nil {
		SendFailureNotification("AssignSlotReward", fmt.Sprintf("Error sending request: %s", err.Error()), time.Now().String(), "Medium")
		log.Errorln("Error sending request:", err.Error())

		// TODO: Check if this should be deleted manually as per need or after a set period of time
		if err = redis.UpdateHashTable(pkgs.RewardsBackendFailures, strconv.Itoa(day), strconv.Itoa(slotId)); err != nil {
			SendFailureNotification("AssignSlotReward", fmt.Sprintf("Error updating failed request for slot %d: %s", slotId, err.Error()), time.Now().String(), "Medium")
			log.Errorf("Error updating failed request for slot %d: %s", slotId, err.Error())
		}
		return
	}
	defer resp.Body.Close()

	// Output the response status and body
	log.Debugln("Response status:", resp.Status)
	var responseBody map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&responseBody)
	log.Debugln("Response body:", responseBody)
}
