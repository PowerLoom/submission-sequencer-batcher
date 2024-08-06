package clients

import (
	"bytes"
	"collector/config"
	"crypto/tls"
	"encoding/json"
	log "github.com/sirupsen/logrus"
	"net/http"
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
		Token:      config.SettingsObj.AuthReadToken,
		SlotID:     slotId,
		DayCounter: day,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		log.Errorln("Error marshaling JSON:", err)
		return
	}

	req, err := http.NewRequest("POST", config.SettingsObj.RewardsBackendUrl, bytes.NewBuffer(jsonData))
	if err != nil {
		log.Errorln("Error creating request:", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := rewardsBackendClient.Do(req)
	if err != nil {
		log.Errorln("Error sending request:", err)
		return
	}
	defer resp.Body.Close()

	// Output the response status and body
	log.Debugln("Response status:", resp.Status)
	var responseBody map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&responseBody)
	log.Debugln("Response body:", responseBody)
}
