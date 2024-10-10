package clients

import (
	"bytes"
	"collector/config"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"time"
)

type TxRelayerClient struct {
	url    string
	client *http.Client
}

type SubmissionBatchSizeRequest struct {
	EpochId   *big.Int `json:"epochId"`
	Size      int      `json:"batchSize"`
	AuthToken string   `json:"authToken"`
}

type SubmitSubmissionBatchRequest struct {
	DataMarketAddress     string   `json:"dataMarket"`
	BatchCid              string   `json:"batchCid"`
	EpochId               *big.Int `json:"epochId"`
	ProjectIds            []string `json:"projectIds"`
	SnapshotCids          []string `json:"snapshotCids"`
	FinalizedCidsRootHash string   `json:"finalizedCidsRootHash"`
	AuthToken             string   `json:"authToken"`
}

var txRelayerClient *TxRelayerClient

func InitializeTxClient(url string, timeout time.Duration) {
	txRelayerClient = &TxRelayerClient{
		url: url,
		client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}
}

func SendSubmissionBatchSize(epochId *big.Int, size int) error {
	request := SubmissionBatchSizeRequest{
		EpochId:   epochId,
		Size:      size,
		AuthToken: config.SettingsObj.TxRelayerAuthWriteToken,
	}

	jsonData, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("unable to marshal notification: %w", err)
	}

	// Concatenate the base URL with the path
	url := fmt.Sprintf("%s/submitBatchSize", txRelayerClient.url)

	resp, err := txRelayerClient.client.Post(url, "application/json", bytes.NewBuffer(jsonData))

	if err != nil {
		return fmt.Errorf("unable to send submission batch size request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to send submission batch size request, status code: %d", resp.StatusCode)
	}

	return nil
}

func SubmitSubmissionBatch(dataMarketAddress string, batchCid string, epochId *big.Int, projectIds []string, snapshotCids []string, finalizedCidsRootHash string) error {
	request := SubmitSubmissionBatchRequest{
		DataMarketAddress:     dataMarketAddress,
		BatchCid:              batchCid,
		EpochId:               epochId,
		ProjectIds:            projectIds,
		SnapshotCids:          snapshotCids,
		FinalizedCidsRootHash: fmt.Sprintf("0x%x", finalizedCidsRootHash),
		AuthToken:             config.SettingsObj.TxRelayerAuthWriteToken,
	}

	jsonData, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("unable to marshal notification: %w", err)
	}

	url := fmt.Sprintf("%s/submitSubmissionBatch", txRelayerClient.url)

	resp, err := txRelayerClient.client.Post(url, "application/json", bytes.NewBuffer(jsonData))

	if err != nil {
		return fmt.Errorf("unable to send submission batch request: %w", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to send submission batch request, status code: %d", resp.StatusCode)
	}

	return nil
}
