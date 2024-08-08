package service

import (
	"bytes"
	"collector/config"
	"collector/pkgs"
	"collector/pkgs/helpers/prost"
	"collector/pkgs/helpers/redis"
	"collector/pkgs/helpers/utils"
	"context"
	"encoding/json"
	"github.com/alicebob/miniredis/v2"
	redisv8 "github.com/go-redis/redis/v8"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

var mr *miniredis.Miniredis

func TestMain(m *testing.M) {
	var err error
	mr, err = miniredis.Run()
	if err != nil {
		log.Fatalf("could not start miniredis: %v", err)
	}

	// Initialize the config settings
	config.SettingsObj = &config.Settings{
		ContractAddress: "0x10c5E2ee14006B3860d4FdF6B173A30553ea6333",
		ClientUrl:       "https://rpc-prost1h-proxy.powerloom.io",
		AuthReadToken:   "valid-token",
		RedisHost:       mr.Host(),
		RedisPort:       mr.Port(),
		RedisDB:         "0",
	}

	redis.RedisClient = redis.NewRedisClient()
	utils.InitLogger()

	prost.ConfigureClient()
	prost.ConfigureContractInstance()

	m.Run()

	mr.Close()
}

func setupRedisForGetTotalRewardsTest(t *testing.T, slotID int, totalRewards string) {
	t.Helper()

	// Create a mini Redis server for testing
	mr.FlushDB()

	redis.RedisClient = redisv8.NewClient(&redisv8.Options{
		Addr: mr.Addr(),
	})
	utils.InitLogger()

	// Populate Redis with test data
	key := redis.TotalSlotRewards(strconv.Itoa(slotID))
	err := redis.Set(context.Background(), key, totalRewards, 0)
	if err != nil {
		t.Fatalf("Failed to set test data in redis: %v", err)
	}
}

func TestHandleTotalRewards(t *testing.T) {
	tests := []struct {
		name           string
		slotID         int
		totalRewards   string
		expectedStatus int
		expectedReward int
	}{
		{
			name:           "Valid total rewards",
			slotID:         1,
			totalRewards:   "500",
			expectedStatus: http.StatusOK,
			expectedReward: 500,
		},
		{
			name:           "Empty total rewards",
			slotID:         2,
			totalRewards:   "",
			expectedStatus: http.StatusInternalServerError,
			expectedReward: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupRedisForGetTotalRewardsTest(t, tt.slotID, tt.totalRewards)

			// Set up a valid request body
			requestBody := RewardsRequest{
				SlotID: tt.slotID,
				Token:  config.SettingsObj.AuthReadToken,
			}
			reqBody, err := json.Marshal(requestBody)
			if err != nil {
				t.Fatalf("could not marshal request body: %v", err)
			}

			// Create a new HTTP request
			req, err := http.NewRequest("POST", "/getTotalRewards", bytes.NewBuffer(reqBody))
			if err != nil {
				t.Fatalf("could not create HTTP request: %v", err)
			}

			// Create a ResponseRecorder to record the response
			rr := httptest.NewRecorder()
			handler := http.HandlerFunc(handleTotalRewards)

			// Wrap the handler with the middleware
			testHandler := RequestMiddleware(handler)

			// Call the handler
			testHandler.ServeHTTP(rr, req)

			responseBody := rr.Body.String()
			t.Log("Response Body:", responseBody)

			// Check the status code
			if status := rr.Code; status != tt.expectedStatus {
				t.Errorf("handler returned wrong status code: got %v want %v", status, tt.expectedStatus)
			}

			// Parse the response body
			var response struct {
				Info struct {
					Success  bool `json:"success"`
					Response int  `json:"response"`
				} `json:"info"`
				RequestID string `json:"request_id"`
			}
			err = json.NewDecoder(rr.Body).Decode(&response)
			if err != nil {
				t.Fatalf("could not decode response body: %v", err)
			}

			// Validate the response
			if tt.expectedStatus == http.StatusOK && !response.Info.Success {
				t.Errorf("response success should be true")
			}
			if response.Info.Response != tt.expectedReward {
				t.Errorf("response rewards should match expected value: got %v want %v", response.Info.Response, tt.expectedReward)
			}
		})
	}
}

func setupRedisForDailyRewardsTest(t *testing.T, slotID int, day int, dailySubmissionCount string) {
	t.Helper()

	// Create a mini Redis server for testing
	mr.FlushDB()

	redis.RedisClient = redisv8.NewClient(&redisv8.Options{
		Addr: mr.Addr(),
	})
	utils.InitLogger()

	// Mock daily snapshot quota and reward base points
	dailySnapshotQuota = big.NewInt(10)
	rewardBasePoints = new(big.Int).Mul(big.NewInt(1000), big.NewInt(baseExponent))

	// Populate Redis with test data
	key := redis.SlotSubmissionKey(strconv.Itoa(slotID), strconv.Itoa(day))
	err := redis.Set(context.Background(), key, dailySubmissionCount, 0)
	if err != nil {
		t.Fatalf("Failed to set test data in redis: %v", err)
	}
}

func TestHandleDailyRewards(t *testing.T) {
	tests := []struct {
		name                  string
		slotID                int
		day                   int
		dailySubmissionCount  string
		expectedStatus        int
		expectedReward        int64
		expectedSuccessStatus bool
	}{
		{
			name:                  "Valid daily rewards",
			slotID:                1,
			day:                   10,
			dailySubmissionCount:  "15",
			expectedStatus:        http.StatusOK,
			expectedReward:        1000,
			expectedSuccessStatus: true,
		},
		{
			name:                  "Empty daily submission count",
			slotID:                2,
			day:                   10,
			dailySubmissionCount:  "",
			expectedStatus:        http.StatusInternalServerError,
			expectedReward:        0,
			expectedSuccessStatus: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupRedisForDailyRewardsTest(t, tt.slotID, tt.day, tt.dailySubmissionCount)

			// Set up a valid request body
			requestBody := DailyRewardsRequest{
				SlotID: tt.slotID,
				Day:    tt.day,
				Token:  config.SettingsObj.AuthReadToken,
			}
			reqBody, err := json.Marshal(requestBody)
			if err != nil {
				t.Fatalf("could not marshal request body: %v", err)
			}

			// Create a new HTTP request
			req, err := http.NewRequest("POST", "/getDailyRewards", bytes.NewBuffer(reqBody))
			if err != nil {
				t.Fatalf("could not create HTTP request: %v", err)
			}

			// Create a ResponseRecorder to record the response
			rr := httptest.NewRecorder()
			handler := http.HandlerFunc(handleDailyRewards)

			// Wrap the handler with the middleware
			testHandler := RequestMiddleware(handler)

			// Call the handler
			testHandler.ServeHTTP(rr, req)

			responseBody := rr.Body.String()
			t.Log("Response Body:", responseBody)

			// Check the status code
			if status := rr.Code; status != tt.expectedStatus {
				t.Errorf("handler returned wrong status code: got %v want %v", status, tt.expectedStatus)
			}

			// Parse the response body
			var response struct {
				Info struct {
					Success  bool `json:"success"`
					Response int  `json:"response"`
				} `json:"info"`
				RequestID string `json:"request_id"`
			}
			err = json.NewDecoder(rr.Body).Decode(&response)
			if err != nil {
				t.Fatalf("could not decode response body: %v", err)
			}

			// Validate the response
			if response.Info.Success != tt.expectedSuccessStatus {
				t.Errorf("response success status should match expected value: got %v want %v", response.Info.Success, tt.expectedSuccessStatus)
			}
			if int64(response.Info.Response) != tt.expectedReward {
				t.Errorf("response rewards should match expected value: got %v want %v", response.Info.Response, tt.expectedReward)
			}
		})
	}
}

func TestHandleTotalSubmissions(t *testing.T) {
	config.SettingsObj.AuthReadToken = "valid-token"
	redis.Set(context.Background(), pkgs.SequencerDayKey, "5", 0)

	redis.Set(context.Background(), redis.SlotSubmissionKey("1", "5"), "100", 0)
	redis.Set(context.Background(), redis.SlotSubmissionKey("1", "4"), "120", 0)
	redis.Set(context.Background(), redis.SlotSubmissionKey("1", "3"), "80", 0)
	redis.Set(context.Background(), redis.SlotSubmissionKey("1", "2"), "400", 0)
	redis.Set(context.Background(), redis.SlotSubmissionKey("1", "1"), "25", 0)

	tests := []struct {
		name       string
		body       string
		statusCode int
		response   []DailySubmissions
	}{
		{
			name:       "Valid token, past days 1",
			body:       `{"slot_id": 1, "token": "valid-token", "past_days": 1}`,
			statusCode: http.StatusOK,
			response: []DailySubmissions{
				{Day: 5, Submissions: 100},
			},
		},
		{
			name:       "Valid token, past days 3",
			body:       `{"slot_id": 1, "token": "valid-token", "past_days": 3}`,
			statusCode: http.StatusOK,
			response: []DailySubmissions{
				{Day: 5, Submissions: 100},
				{Day: 4, Submissions: 120},
				{Day: 3, Submissions: 80},
			},
		},
		{
			name:       "Valid token, total submissions till date",
			body:       `{"slot_id": 1, "token": "valid-token", "past_days": 5}`,
			statusCode: http.StatusOK,
			response: []DailySubmissions{
				{Day: 5, Submissions: 100},
				{Day: 4, Submissions: 120},
				{Day: 3, Submissions: 80},
				{Day: 2, Submissions: 400},
				{Day: 1, Submissions: 25},
			},
		},
		{
			name:       "Valid token, negative past days",
			body:       `{"slot_id": 1, "token": "valid-token", "past_days": -1}`,
			statusCode: http.StatusBadRequest,
			response:   nil,
		},
		{
			name:       "Invalid token",
			body:       `{"slot_id": 1, "token": "invalid-token", "past_days": 1}`,
			statusCode: http.StatusUnauthorized,
			response:   nil,
		},
		{
			name:       "Invalid slot ID",
			body:       `{"slot_id": 10001, "token": "valid-token", "past_days": 1}`,
			statusCode: http.StatusBadRequest,
			response:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest("POST", "/totalSubmissions", strings.NewReader(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")

			rr := httptest.NewRecorder()
			handler := http.HandlerFunc(handleTotalSubmissions)
			testHandler := RequestMiddleware(handler)
			testHandler.ServeHTTP(rr, req)

			responseBody := rr.Body.String()
			t.Log("Response Body:", responseBody)

			assert.Equal(t, tt.statusCode, rr.Code)

			if tt.statusCode == http.StatusOK {
				var response struct {
					Info struct {
						Success  bool               `json:"success"`
						Response []DailySubmissions `json:"response"`
					} `json:"info"`
					RequestID string `json:"request_id"`
				}
				err = json.NewDecoder(rr.Body).Decode(&response)

				err := json.Unmarshal([]byte(responseBody), &response)
				assert.NoError(t, err)
				assert.Equal(t, tt.response, response.Info.Response)
			}
		})
	}
}

func TestHandleFinalizedBatchSubmissions(t *testing.T) {
	config.SettingsObj.AuthReadToken = "valid-token"
	redis.SetProcessLog(context.Background(),
		redis.TriggeredProcessLog(pkgs.FinalizeBatches, "1"),
		map[string]interface{}{
			"epoch_id":                "1",
			"finalized_batches_count": 3,
			"finalized_batch_ids":     []string{"1", "2", "3"},
			"timestamp":               float64(123456),
		},
		0,
	)

	redis.SetProcessLog(context.Background(),
		redis.TriggeredProcessLog(pkgs.FinalizeBatches, "2"),
		map[string]interface{}{
			"epoch_id":                "2",
			"finalized_batches_count": 1,
			"finalized_batch_ids":     []string{"4"},
			"timestamp":               float64(12345678),
		},
		0,
	)

	redis.SetProcessLog(context.Background(),
		redis.TriggeredProcessLog(pkgs.FinalizeBatches, "3"),
		map[string]interface{}{
			"epoch_id":                "3",
			"finalized_batches_count": 5,
			"finalized_batch_ids":     []string{"5", "6", "7", "8", "9"},
			"timestamp":               float64(123456789),
		},
		0,
	)

	tests := []struct {
		name       string
		body       string
		statusCode int
		response   []map[string]interface{}
	}{
		{
			name:       "Valid token, past epochs 1",
			body:       `{"token": "valid-token", "past_epochs": 1}`,
			statusCode: http.StatusOK,
			response: []map[string]interface{}{
				{
					"epoch_id":                "3",
					"finalized_batches_count": 5,
					"finalized_batch_ids":     []interface{}{"5", "6", "7", "8", "9"},
					"timestamp":               float64(123456789),
				},
			},
		},
		{
			name:       "Valid token, past epochs 0",
			body:       `{"token": "valid-token", "past_batches": 0}`,
			statusCode: http.StatusOK,
			response: []map[string]interface{}{
				{
					"epoch_id":                "1",
					"finalized_batches_count": 3,
					"finalized_batch_ids":     []string{"1", "2", "3"},
					"timestamp":               float64(123456),
				},
				{
					"epoch_id":                "2",
					"finalized_batches_count": 1,
					"finalized_batch_ids":     []string{"4"},
					"timestamp":               float64(12345678),
				},
				{
					"epoch_id":                "3",
					"finalized_batches_count": 5,
					"finalized_batch_ids":     []interface{}{"5", "6", "7", "8", "9"},
					"timestamp":               float64(123456789),
				},
			},
		},
		{
			name:       "Invalid token",
			body:       `{"token": "invalid-token", "past_epochs": 1}`,
			statusCode: http.StatusUnauthorized,
			response:   nil,
		},
		{
			name:       "Negative past batches",
			body:       `{"token": "valid-token", "past_epochs": -1}`,
			statusCode: http.StatusBadRequest,
			response:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest("POST", "/finalizedBatchSubmissions", strings.NewReader(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")

			rr := httptest.NewRecorder()
			handler := http.HandlerFunc(handleFinalizedBatchSubmissions)
			testHandler := RequestMiddleware(handler)
			testHandler.ServeHTTP(rr, req)

			responseBody := rr.Body.String()
			t.Log("Response Body:", responseBody)

			assert.Equal(t, tt.statusCode, rr.Code)

			if tt.statusCode == http.StatusOK {
				var response struct {
					Info struct {
						Success  bool                     `json:"success"`
						Response []map[string]interface{} `json:"response"`
					} `json:"info"`
					RequestID string `json:"request_id"`
				}
				err = json.NewDecoder(rr.Body).Decode(&response)
				assert.NoError(t, err)
				actualResp, _ := json.Marshal(tt.response)
				expectedResp, _ := json.Marshal(response.Info.Response)
				assert.Equal(t, expectedResp, actualResp)
			}
		})
	}
}

func TestHandleTriggeredCollectionFlows(t *testing.T) {
	config.SettingsObj.AuthReadToken = "valid-token"
	redis.SetProcessLog(context.Background(),
		redis.TriggeredProcessLog(pkgs.TriggerCollectionFlow, "1"),
		map[string]interface{}{
			"epoch_id":      "12",
			"start_block":   "100",
			"current_block": "220",
			"header_count":  "120",
			"timestamp":     float64(123456),
		},
		0,
	)

	redis.SetProcessLog(context.Background(),
		redis.TriggeredProcessLog(pkgs.TriggerCollectionFlow, "2"),
		map[string]interface{}{
			"epoch_id":      "13",
			"start_block":   "220",
			"current_block": "340",
			"header_count":  "120",
			"timestamp":     float64(12345678),
		},
		0,
	)

	redis.SetProcessLog(context.Background(),
		redis.TriggeredProcessLog(pkgs.TriggerCollectionFlow, "3"),
		map[string]interface{}{
			"epoch_id":      "14",
			"start_block":   "340",
			"current_block": "460",
			"header_count":  "120",
			"timestamp":     float64(123456789),
		},
		0,
	)

	tests := []struct {
		name       string
		body       string
		statusCode int
		response   []map[string]interface{}
	}{
		{
			name:       "Valid token, past epochs 1",
			body:       `{"token": "valid-token", "past_epochs": 1}`,
			statusCode: http.StatusOK,
			response: []map[string]interface{}{
				{
					"epoch_id":      "14",
					"start_block":   "340",
					"current_block": "460",
					"header_count":  "120",
					"timestamp":     float64(123456789),
				},
			},
		},
		{
			name:       "Valid token, past epochs 0",
			body:       `{"token": "valid-token", "past_epochs": 0}`,
			statusCode: http.StatusOK,
			response: []map[string]interface{}{
				{
					"epoch_id":      "12",
					"start_block":   "100",
					"current_block": "220",
					"header_count":  "120",
					"timestamp":     float64(123456),
				},
				{
					"epoch_id":      "13",
					"start_block":   "220",
					"current_block": "340",
					"header_count":  "120",
					"timestamp":     float64(12345678),
				},
				{
					"epoch_id":      "14",
					"start_block":   "340",
					"current_block": "460",
					"header_count":  "120",
					"timestamp":     float64(123456789),
				},
			},
		},
		{
			name:       "Invalid token",
			body:       `{"token": "invalid-token", "past_epochs": 1}`,
			statusCode: http.StatusUnauthorized,
			response:   nil,
		},
		{
			name:       "Negative past epochs",
			body:       `{"token": "valid-token", "past_epochs": -1}`,
			statusCode: http.StatusBadRequest,
			response:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest("POST", "/triggeredCollectionFlows", strings.NewReader(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")

			rr := httptest.NewRecorder()
			handler := http.HandlerFunc(handleTriggeredCollectionFlows)
			testHandler := RequestMiddleware(handler)
			testHandler.ServeHTTP(rr, req)

			responseBody := rr.Body.String()
			t.Log("Response Body:", responseBody)

			assert.Equal(t, tt.statusCode, rr.Code)

			if tt.statusCode == http.StatusOK {
				var response struct {
					Info struct {
						Success  bool                     `json:"success"`
						Response []map[string]interface{} `json:"response"`
					} `json:"info"`
					RequestID string `json:"request_id"`
				}
				err = json.NewDecoder(rr.Body).Decode(&response)
				assert.NoError(t, err)
				actualResp, _ := json.Marshal(tt.response)
				expectedResp, _ := json.Marshal(response.Info.Response)
				assert.Equal(t, expectedResp, actualResp)
			}
		})
	}
}

func TestHandleBuiltBatches(t *testing.T) {
	config.SettingsObj.AuthReadToken = "valid-token"
	redis.SetProcessLog(context.Background(),
		redis.TriggeredProcessLog(pkgs.BuildBatch, "1"),
		map[string]interface{}{
			"epoch_id":          "1",
			"batch_id":          1,
			"batch_cid":         "cid1",
			"submissions_count": 3,
			"submissions":       []interface{}{"sub1", "sub2", "sub3"},
			"timestamp":         float64(123456),
		},
		0,
	)

	redis.SetProcessLog(context.Background(),
		redis.TriggeredProcessLog(pkgs.BuildBatch, "2"),
		map[string]interface{}{
			"epoch_id":          "2",
			"batch_id":          2,
			"batch_cid":         "cid2",
			"submissions_count": 1,
			"submissions":       []interface{}{"sub4"},
			"timestamp":         float64(12345678),
		},
		0,
	)

	redis.SetProcessLog(context.Background(),
		redis.TriggeredProcessLog(pkgs.BuildBatch, "3"),
		map[string]interface{}{
			"epoch_id":          "3",
			"batch_id":          3,
			"batch_cid":         "cid3",
			"submissions_count": 5,
			"submissions":       []interface{}{"sub5", "sub6", "sub7", "sub8", "sub9"},
			"timestamp":         float64(123456789),
		},
		0,
	)

	tests := []struct {
		name       string
		body       string
		statusCode int
		response   []map[string]interface{}
	}{
		{
			name:       "Valid token, past batches 1",
			body:       `{"token": "valid-token", "past_batches": 1}`,
			statusCode: http.StatusOK,
			response: []map[string]interface{}{
				{
					"epoch_id":          "3",
					"batch_id":          float64(3),
					"batch_cid":         "cid3",
					"submissions_count": float64(5),
					"submissions":       []interface{}{"sub5", "sub6", "sub7", "sub8", "sub9"},
					"timestamp":         float64(123456789),
				},
			},
		},
		{
			name:       "Valid token, past batches 0",
			body:       `{"token": "valid-token", "past_batches": 0}`,
			statusCode: http.StatusOK,
			response: []map[string]interface{}{
				{
					"epoch_id":          "1",
					"batch_id":          float64(1),
					"batch_cid":         "cid1",
					"submissions_count": float64(3),
					"submissions":       []interface{}{"sub1", "sub2", "sub3"},
					"timestamp":         float64(123456),
				},
				{
					"epoch_id":          "2",
					"batch_id":          float64(2),
					"batch_cid":         "cid2",
					"submissions_count": float64(1),
					"submissions":       []interface{}{"sub4"},
					"timestamp":         float64(12345678),
				},
				{
					"epoch_id":          "3",
					"batch_id":          float64(3),
					"batch_cid":         "cid3",
					"submissions_count": float64(5),
					"submissions":       []interface{}{"sub5", "sub6", "sub7", "sub8", "sub9"},
					"timestamp":         float64(123456789),
				},
			},
		},
		{
			name:       "Invalid token",
			body:       `{"token": "invalid-token", "past_batches": 1}`,
			statusCode: http.StatusUnauthorized,
			response:   nil,
		},
		{
			name:       "Negative past batches",
			body:       `{"token": "valid-token", "past_batches": -1}`,
			statusCode: http.StatusBadRequest,
			response:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest("POST", "/builtBatches", strings.NewReader(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")

			rr := httptest.NewRecorder()
			handler := http.HandlerFunc(handleBuiltBatches)
			testHandler := RequestMiddleware(handler)
			testHandler.ServeHTTP(rr, req)

			responseBody := rr.Body.String()
			t.Log("Response Body:", responseBody)

			assert.Equal(t, tt.statusCode, rr.Code)

			if tt.statusCode == http.StatusOK {
				var response struct {
					Info struct {
						Success  bool                     `json:"success"`
						Response []map[string]interface{} `json:"response"`
					} `json:"info"`
					RequestID string `json:"request_id"`
				}
				err = json.NewDecoder(rr.Body).Decode(&response)
				assert.NoError(t, err)
				actualResp, _ := json.Marshal(tt.response)
				expectedResp, _ := json.Marshal(response.Info.Response)
				assert.Equal(t, expectedResp, actualResp)
			}
		})
	}
}

func TestHandleCommittedSubmissionBatches(t *testing.T) {
	config.SettingsObj.AuthReadToken = "valid-token"
	redis.SetProcessLog(context.Background(),
		redis.TriggeredProcessLog(pkgs.CommitSubmissionBatch, "1"),
		map[string]interface{}{
			"epoch_id":  "1",
			"batch_id":  1,
			"tx_hash":   "0x123",
			"signer":    "0xabc",
			"nonce":     "1",
			"timestamp": float64(123456),
		},
		0,
	)

	redis.SetProcessLog(context.Background(),
		redis.TriggeredProcessLog(pkgs.CommitSubmissionBatch, "2"),
		map[string]interface{}{
			"epoch_id":  "2",
			"batch_id":  2,
			"tx_hash":   "0x456",
			"signer":    "0xdef",
			"nonce":     "2",
			"timestamp": float64(12345678),
		},
		0,
	)

	redis.SetProcessLog(context.Background(),
		redis.TriggeredProcessLog(pkgs.CommitSubmissionBatch, "3"),
		map[string]interface{}{
			"epoch_id":  "3",
			"batch_id":  3,
			"tx_hash":   "0x789",
			"signer":    "0xghi",
			"nonce":     "3",
			"timestamp": float64(123456789),
		},
		0,
	)

	tests := []struct {
		name       string
		body       string
		statusCode int
		response   []map[string]interface{}
	}{
		{
			name:       "Valid token, past batches 1",
			body:       `{"token": "valid-token", "past_batches": 1}`,
			statusCode: http.StatusOK,
			response: []map[string]interface{}{
				{
					"epoch_id":  "3",
					"batch_id":  float64(3),
					"tx_hash":   "0x789",
					"signer":    "0xghi",
					"nonce":     "3",
					"timestamp": float64(123456789),
				},
			},
		},
		{
			name:       "Valid token, past batches 0",
			body:       `{"token": "valid-token", "past_batches": 0}`,
			statusCode: http.StatusOK,
			response: []map[string]interface{}{
				{
					"epoch_id":  "1",
					"batch_id":  float64(1),
					"tx_hash":   "0x123",
					"signer":    "0xabc",
					"nonce":     "1",
					"timestamp": float64(123456),
				},
				{
					"epoch_id":  "2",
					"batch_id":  float64(2),
					"tx_hash":   "0x456",
					"signer":    "0xdef",
					"nonce":     "2",
					"timestamp": float64(12345678),
				},
				{
					"epoch_id":  "3",
					"batch_id":  float64(3),
					"tx_hash":   "0x789",
					"signer":    "0xghi",
					"nonce":     "3",
					"timestamp": float64(123456789),
				},
			},
		},
		{
			name:       "Invalid token",
			body:       `{"token": "invalid-token", "past_batches": 1}`,
			statusCode: http.StatusUnauthorized,
			response:   nil,
		},
		{
			name:       "Negative past batches",
			body:       `{"token": "valid-token", "past_batches": -1}`,
			statusCode: http.StatusBadRequest,
			response:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest("POST", "/committedSubmissionBatches", strings.NewReader(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")

			rr := httptest.NewRecorder()
			handler := http.HandlerFunc(handleCommittedSubmissionBatches)
			testHandler := RequestMiddleware(handler)
			testHandler.ServeHTTP(rr, req)

			responseBody := rr.Body.String()
			t.Log("Response Body:", responseBody)

			assert.Equal(t, tt.statusCode, rr.Code)

			if tt.statusCode == http.StatusOK {
				var response struct {
					Info struct {
						Success  bool                     `json:"success"`
						Response []map[string]interface{} `json:"response"`
					} `json:"info"`
					RequestID string `json:"request_id"`
				}
				err = json.NewDecoder(rr.Body).Decode(&response)
				assert.NoError(t, err)
				actualResp, _ := json.Marshal(tt.response)
				expectedResp, _ := json.Marshal(response.Info.Response)
				assert.Equal(t, expectedResp, actualResp)
			}
		})
	}
}

func TestHandleBatchResubmissions(t *testing.T) {
	config.SettingsObj.AuthReadToken = "valid-token"
	redis.SetProcessLog(context.Background(),
		redis.TriggeredProcessLog(pkgs.EnsureBatchSubmissionSuccess, "1"),
		map[string]interface{}{
			"epoch_id":  "1",
			"batch_id":  1,
			"tx_hash":   "0x123",
			"signer":    "0xabc",
			"nonce":     "1",
			"timestamp": float64(123456),
		},
		0,
	)

	redis.SetProcessLog(context.Background(),
		redis.TriggeredProcessLog(pkgs.EnsureBatchSubmissionSuccess, "2"),
		map[string]interface{}{
			"epoch_id":  "2",
			"batch_id":  2,
			"tx_hash":   "0x456",
			"signer":    "0xdef",
			"nonce":     "2",
			"timestamp": float64(12345678),
		},
		0,
	)

	redis.SetProcessLog(context.Background(),
		redis.TriggeredProcessLog(pkgs.EnsureBatchSubmissionSuccess, "3"),
		map[string]interface{}{
			"epoch_id":  "3",
			"batch_id":  3,
			"tx_hash":   "0x789",
			"signer":    "0xghi",
			"nonce":     "3",
			"timestamp": float64(123456789),
		},
		0,
	)

	tests := []struct {
		name       string
		body       string
		statusCode int
		response   []map[string]interface{}
	}{
		{
			name:       "Valid token, past batches 1",
			body:       `{"token": "valid-token", "past_batches": 1}`,
			statusCode: http.StatusOK,
			response: []map[string]interface{}{
				{
					"epoch_id":  "3",
					"batch_id":  float64(3),
					"tx_hash":   "0x789",
					"signer":    "0xghi",
					"nonce":     "3",
					"timestamp": float64(123456789),
				},
			},
		},
		{
			name:       "Valid token, past batches 0",
			body:       `{"token": "valid-token", "past_batches": 0}`,
			statusCode: http.StatusOK,
			response: []map[string]interface{}{
				{
					"epoch_id":  "1",
					"batch_id":  float64(1),
					"tx_hash":   "0x123",
					"signer":    "0xabc",
					"nonce":     "1",
					"timestamp": float64(123456),
				},
				{
					"epoch_id":  "2",
					"batch_id":  float64(2),
					"tx_hash":   "0x456",
					"signer":    "0xdef",
					"nonce":     "2",
					"timestamp": float64(12345678),
				},
				{
					"epoch_id":  "3",
					"batch_id":  float64(3),
					"tx_hash":   "0x789",
					"signer":    "0xghi",
					"nonce":     "3",
					"timestamp": float64(123456789),
				},
			},
		},
		{
			name:       "Invalid token",
			body:       `{"token": "invalid-token", "past_batches": 1}`,
			statusCode: http.StatusUnauthorized,
			response:   nil,
		},
		{
			name:       "Negative past batches",
			body:       `{"token": "valid-token", "past_batches": -1}`,
			statusCode: http.StatusBadRequest,
			response:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest("POST", "/batchResubmissions", strings.NewReader(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")

			rr := httptest.NewRecorder()
			handler := http.HandlerFunc(handleBatchResubmissions)
			testHandler := RequestMiddleware(handler)
			testHandler.ServeHTTP(rr, req)

			responseBody := rr.Body.String()
			t.Log("Response Body:", responseBody)

			assert.Equal(t, tt.statusCode, rr.Code)

			if tt.statusCode == http.StatusOK {
				var response struct {
					Info struct {
						Success  bool                     `json:"success"`
						Response []map[string]interface{} `json:"response"`
					} `json:"info"`
					RequestID string `json:"request_id"`
				}
				err = json.NewDecoder(rr.Body).Decode(&response)
				assert.NoError(t, err)
				actualResp, _ := json.Marshal(tt.response)
				expectedResp, _ := json.Marshal(response.Info.Response)
				assert.Equal(t, expectedResp, actualResp)
			}
		})
	}
}

func TestHandleIncludedEpochSubmissionsCount(t *testing.T) {
	config.SettingsObj.AuthReadToken = "valid-token"
	redis.SetProcessLog(context.Background(),
		redis.TriggeredProcessLog(pkgs.BuildBatch, "1"),
		map[string]interface{}{
			"epoch_id":          "1",
			"batch_id":          1,
			"batch_cid":         "cid1",
			"submissions_count": 3,
			"submissions":       []interface{}{"sub1", "sub2", "sub3"},
			"timestamp":         float64(123456),
		},
		0,
	)

	redis.SetProcessLog(context.Background(),
		redis.TriggeredProcessLog(pkgs.BuildBatch, "2"),
		map[string]interface{}{
			"epoch_id":          "2",
			"batch_id":          2,
			"batch_cid":         "cid2",
			"submissions_count": 1,
			"submissions":       []interface{}{"sub4"},
			"timestamp":         float64(12345678),
		},
		0,
	)

	redis.SetProcessLog(context.Background(),
		redis.TriggeredProcessLog(pkgs.BuildBatch, "3"),
		map[string]interface{}{
			"epoch_id":          "3",
			"batch_id":          3,
			"batch_cid":         "cid3",
			"submissions_count": 5,
			"submissions":       []interface{}{"sub5", "sub6", "sub7", "sub8", "sub9"},
			"timestamp":         float64(123456789),
		},
		0,
	)

	tests := []struct {
		name       string
		body       string
		statusCode int
		response   int
	}{
		{
			name:       "Valid token, all epochs",
			body:       `{"token": "valid-token", "past_epochs": 0}`,
			statusCode: http.StatusOK,
			response:   9,
		},
		{
			name:       "Valid token, specific epochs",
			body:       `{"token": "valid-token", "past_epochs": 2}`,
			statusCode: http.StatusOK,
			response:   6,
		},
		{
			name:       "Invalid token",
			body:       `{"token": "invalid-token", "past_epochs": 1}`,
			statusCode: http.StatusUnauthorized,
			response:   0,
		},
		{
			name:       "Negative past epochs",
			body:       `{"token": "valid-token", "past_epochs": -1}`,
			statusCode: http.StatusBadRequest,
			response:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest("POST", "/includedEpochSubmissions", strings.NewReader(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")

			rr := httptest.NewRecorder()
			handler := http.HandlerFunc(handleIncludedEpochSubmissionsCount)
			testHandler := RequestMiddleware(handler)
			testHandler.ServeHTTP(rr, req)

			responseBody := rr.Body.String()
			t.Log("Response Body:", responseBody)

			assert.Equal(t, tt.statusCode, rr.Code)

			if tt.statusCode == http.StatusOK {
				var response struct {
					Info struct {
						Success  bool `json:"success"`
						Response int  `json:"response"`
					} `json:"info"`
					RequestID string `json:"request_id"`
				}
				err = json.NewDecoder(rr.Body).Decode(&response)
				assert.NoError(t, err)
				assert.Equal(t, tt.response, response.Info.Response)
			}
		})
	}
}

func TestHandleReceivedEpochSubmissionsCount(t *testing.T) {
	config.SettingsObj.AuthReadToken = "valid-token"
	redis.Set(context.Background(), redis.EpochSubmissionsCount(1), "3", time.Hour)
	redis.Set(context.Background(), redis.EpochSubmissionsCount(2), "1", time.Hour)
	redis.Set(context.Background(), redis.EpochSubmissionsCount(3), "5", time.Hour)

	tests := []struct {
		name       string
		body       string
		statusCode int
		response   int
	}{
		{
			name:       "Valid token, all epochs",
			body:       `{"token": "valid-token", "past_epochs": 0}`,
			statusCode: http.StatusOK,
			response:   9,
		},
		{
			name:       "Valid token, specific epochs",
			body:       `{"token": "valid-token", "past_epochs": 2}`,
			statusCode: http.StatusOK,
			response:   6,
		},
		{
			name:       "Invalid token",
			body:       `{"token": "invalid-token", "past_epochs": 1}`,
			statusCode: http.StatusUnauthorized,
			response:   0,
		},
		{
			name:       "Negative past epochs",
			body:       `{"token": "valid-token", "past_epochs": -1}`,
			statusCode: http.StatusBadRequest,
			response:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest("POST", "/receivedEpochSubmissionsCount", strings.NewReader(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")

			rr := httptest.NewRecorder()
			handler := http.HandlerFunc(handleReceivedEpochSubmissionsCount)
			testHandler := RequestMiddleware(handler)
			testHandler.ServeHTTP(rr, req)

			responseBody := rr.Body.String()
			t.Log("Response Body:", responseBody)

			assert.Equal(t, tt.statusCode, rr.Code)

			if tt.statusCode == http.StatusOK {
				var response struct {
					Info struct {
						Success  bool `json:"success"`
						Response int  `json:"response"`
					} `json:"info"`
					RequestID string `json:"request_id"`
				}
				err = json.NewDecoder(rr.Body).Decode(&response)
				assert.NoError(t, err)
				assert.Equal(t, tt.response, response.Info.Response)
			}
		})
	}
}

func TestHandleReceivedEpochSubmissions(t *testing.T) {
	config.SettingsObj.AuthReadToken = "valid-token"

	// Set test data in Redis
	redis.RedisClient.HSet(context.Background(), redis.EpochSubmissionsKey(1), "submission1", `{"request": {"slotId": 1, "epochId": 1}}`)
	redis.RedisClient.HSet(context.Background(), redis.EpochSubmissionsKey(1), "submission2", `{"request": {"slotId": 1, "epochId": 1}}`)
	redis.RedisClient.HSet(context.Background(), redis.EpochSubmissionsKey(2), "submission3", `{"request": {"slotId": 1, "epochId": 2}}`)
	redis.RedisClient.HSet(context.Background(), redis.EpochSubmissionsKey(2), "submission4", `{"request": {"slotId": 1, "epochId": 2}}`)
	redis.RedisClient.HSet(context.Background(), redis.EpochSubmissionsKey(3), "submission5", `{"request": {"slotId": 1, "epochId": 3}}`)

	tests := []struct {
		name       string
		body       string
		statusCode int
		response   []map[string]interface{}
	}{
		{
			name:       "Valid token, past epochs 1",
			body:       `{"token": "valid-token", "past_epochs": 1}`,
			statusCode: http.StatusOK,
			response: []map[string]interface{}{
				{
					"epoch_id": "3",
					"submissions": map[string]interface{}{
						"submission5": map[string]interface{}{
							"request": map[string]interface{}{
								"slotId":  1,
								"epochId": 3,
							},
						},
					},
				},
			},
		},
		{
			name:       "Valid token, past epochs 0",
			body:       `{"token": "valid-token", "past_epochs": 0}`,
			statusCode: http.StatusOK,
			response: []map[string]interface{}{
				{
					"epoch_id": "1",
					"submissions": map[string]interface{}{
						"submission1": map[string]interface{}{
							"request": map[string]interface{}{
								"slotId":  1,
								"epochId": 1,
							},
						},
						"submission2": map[string]interface{}{
							"request": map[string]interface{}{
								"slotId":  1,
								"epochId": 1,
							},
						},
					},
				},
				{
					"epoch_id": "2",
					"submissions": map[string]interface{}{
						"submission3": map[string]interface{}{
							"request": map[string]interface{}{
								"slotId":  1,
								"epochId": 2,
							},
						},
						"submission4": map[string]interface{}{
							"request": map[string]interface{}{
								"slotId":  1,
								"epochId": 2,
							},
						},
					},
				},
				{
					"epoch_id": "3",
					"submissions": map[string]interface{}{
						"submission5": map[string]interface{}{
							"request": map[string]interface{}{
								"slotId":  1,
								"epochId": 3,
							},
						},
					},
				},
			},
		},
		{
			name:       "Invalid token",
			body:       `{"token": "invalid-token", "past_epochs": 1}`,
			statusCode: http.StatusUnauthorized,
			response:   nil,
		},
		{
			name:       "Negative past epochs",
			body:       `{"token": "valid-token", "past_epochs": -1}`,
			statusCode: http.StatusBadRequest,
			response:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest("POST", "/receivedEpochSubmissions", strings.NewReader(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")

			rr := httptest.NewRecorder()
			handler := http.HandlerFunc(handleReceivedEpochSubmissions)
			testHandler := RequestMiddleware(handler)
			testHandler.ServeHTTP(rr, req)

			responseBody := rr.Body.String()
			t.Log("Response Body:", responseBody)

			assert.Equal(t, tt.statusCode, rr.Code)

			if tt.statusCode == http.StatusOK {
				var response struct {
					Info struct {
						Success  bool                     `json:"success"`
						Response []map[string]interface{} `json:"response"`
					} `json:"info"`
					RequestID string `json:"request_id"`
				}
				err = json.NewDecoder(rr.Body).Decode(&response)
				assert.NoError(t, err)
				actualResp, _ := json.Marshal(tt.response)
				expectedResp, _ := json.Marshal(response.Info.Response)
				assert.JSONEq(t, string(expectedResp), string(actualResp))
			}
		})
	}
}
