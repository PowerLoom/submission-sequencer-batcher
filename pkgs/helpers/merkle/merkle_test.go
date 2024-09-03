package merkle

import (
	"bufio"
	"bytes"
	"collector/config"
	"collector/pkgs"
	"collector/pkgs/helpers/clients"
	"collector/pkgs/helpers/ipfs"
	"collector/pkgs/helpers/redis"
	"collector/pkgs/helpers/utils"
	"context"
	"fmt"
	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	mh "github.com/multiformats/go-multihash"
	log "github.com/sirupsen/logrus"
	"google.golang.org/protobuf/encoding/protojson"
	"math/big"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

type SubmissionDetails struct {
	day          *big.Int
	submissionId uuid.UUID
	submission   *pkgs.SnapshotSubmission
}

// Setup function emulating beforeEach in JavaScript
func setup(t *testing.T) *miniredis.Miniredis {
	// Initialize a miniredis instance
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("Failed to start miniredis: %v", err)
	}

	// Configure the application to use miniredis
	config.SettingsObj = &config.Settings{
		RedisHost: mr.Host(),
		RedisPort: mr.Port(),
		//RedisHost:         "localhost",
		//RedisPort:         "6379",
		RedisDB:           "0",
		SlackReportingUrl: "https://hooks.slack.com",
		IPFSUrl:           "localhost:5001",
	}

	clients.InitializeReportingClient(config.SettingsObj.SlackReportingUrl, time.Duration(config.SettingsObj.HttpTimeout)*time.Second)

	// Initialize the Redis client
	redis.RedisClient = redis.NewRedisClient()

	// Use t.Cleanup to ensure the miniredis instance is closed after the test
	t.Cleanup(func() {
		redis.RedisClient.FlushAll(context.Background())
		mr.Close()
	})

	return mr
}

func TestRedisConnection(t *testing.T) {
	mr := setup(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	config.SettingsObj = &config.Settings{
		RedisHost: mr.Host(),
		RedisPort: mr.Port(),
		RedisDB:   "0",
	}

	redis.RedisClient = redis.NewRedisClient()

	key := "test_key"
	value := "test_value"

	if err := redis.SetSubmission(ctx, key, value, "test_header", 5*time.Minute); err != nil {
		t.Fatalf("Error setting key-value pair: %v", err)
	}

	storedValue, err := redis.Get(ctx, key)
	if err != nil {
		t.Fatalf("Error getting key-value pair: %v", err)
	}

	if storedValue != value {
		t.Errorf("Expected value %v, but got %v", value, storedValue)
	}
}

func TestIPFSConnection(t *testing.T) {
	_, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	config.SettingsObj = &config.Settings{
		IPFSUrl: "http://localhost:5001",
	}

	ipfs.ConnectIPFSNode()

	// Add a simple IPFS test case
	data := []byte("Hello, IPFS!")
	cid, err := ipfs.IPFSCon.Add(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Error adding data to IPFS: %v", err)
	}

	if cid == "" {
		t.Errorf("Expected CID, but got none")
	}

	log.Debugf("Added data to IPFS with CID: %s", cid)
}

func TestBuildBatchSubmissions(t *testing.T) {
	setup(t)
	_, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	config.SettingsObj.BatchSize = 10

	ipfs.ConnectIPFSNode()

	var cids []string
	var num = 6000

	rand.Seed(time.Now().UnixNano())
	// Generate 10 random CIDs
	for i := 0; i < num; i++ {
		randomBytes := make([]byte, 32)
		rand.Read(randomBytes)

		hash, _ := mh.Sum(randomBytes, mh.SHA2_256, -1)
		cid := hash.B58String()

		cids = append(cids, cid)
	}

	var submissions []SubmissionDetails

	for i := 0; i < num; i++ {
		submission := SubmissionDetails{
			day:          big.NewInt(1),
			submissionId: uuid.New(),
			submission: &pkgs.SnapshotSubmission{
				Request: &pkgs.Request{
					SlotId:      uint64(rand.Int63()),
					Deadline:    uint64(rand.Int63()),
					SnapshotCid: cids[rand.Intn(len(cids))],
					EpochId:     1,
					ProjectId:   "pairContract_trade_volume:0x0d4a11d5eeaac28ec3f61d100daf4d40471f1852:UNISWAPV2" + strconv.Itoa(i%419), // + uuid.New().String(), // Random projectId
				},
				Signature: "signature" + uuid.New().String(),
				Header:    "header1",
			},
		}
		submissions = append(submissions, submission)
	}

	for i := 0; i < num; i++ {
		key := SubmissionKey(submissions[i].submission.Request.EpochId, submissions[i].submission.Request.ProjectId, new(big.Int).SetUint64(submissions[i].submission.Request.SlotId).String())
		value := fmt.Sprintf("%s.%s", submissions[i].submissionId.String(), protojson.Format(submissions[i].submission))
		set := redis.SubmissionSetByHeaderKey(submissions[i].submission.Request.EpochId, submissions[i].submission.Header)

		if err := redis.SetSubmission(context.Background(), key, value, set, 5*time.Minute); err != nil {
			log.Errorln("Error setting key-value pair: ", err.Error())
		}
	}

	batchSubmissions, err := BuildBatchSubmissions(big.NewInt(1), []string{"header1"})
	if err != nil {
		t.Errorf("Expected no error, but got: %v", err)
	}

	t.Log("Batch submissions length: ", len(batchSubmissions))

	// Check the returned value
	if batchSubmissions == nil {
		t.Errorf("Expected batchSubmissions to be not nil")
	}

	//// Check that the submission with the most CID counts is included
	mostCids := 0
	for _, batchSubmission := range batchSubmissions {
		if len(batchSubmission.Batch.Cids) > mostCids {
			mostCids = len(batchSubmission.Batch.Cids)
		}
	}
	if mostCids != len(batchSubmissions[0].Batch.Cids) {
		t.Errorf("Expected mostCids to be equal to %v, but got: %v", len(batchSubmissions[0].Batch.Cids), mostCids)
	}
}

func TestBuildBatchSubmissions_Basic(t *testing.T) {
	setup(t)

	_, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ipfs.ConnectIPFSNode()

	// Create and store mock submissions
	epochID := big.NewInt(1)
	header := "header1"
	projectID := "pairContract_trade_volume:0x0d4a11d5eeaac28ec3f61d100daf4d40471f1852:UNISWAPV2"
	snapshotCid := "QmT5NvUtoM5nXMsCpS1dZXZeaZZ5iZZm7RM5czM11ik7x4"

	submission1 := createMockSubmission(projectID, snapshotCid)
	for i := 0; i < 10; i++ {
		submission1.Request.SlotId += 1
		key1 := SubmissionKey(epochID.Uint64(), projectID, new(big.Int).SetUint64(submission1.Request.SlotId).String())
		value1 := fmt.Sprintf("%s.%s", uuid.New().String(), protojson.Format(submission1))
		set1 := redis.SubmissionSetByHeaderKey(epochID.Uint64(), header)

		if err := redis.SetSubmission(context.Background(), key1, value1, set1, 5*time.Minute); err != nil {
			t.Fatalf("Error setting mock submission: %v", err)
		}
	}
	// Repeat above steps to create more submissions as needed

	batchSubmissions, err := BuildBatchSubmissions(epochID, []string{header})
	if err != nil {
		t.Errorf("Expected no error, but got: %v", err)
	}

	if len(batchSubmissions) == 0 {
		t.Errorf("Expected batch submissions, but got none")
	}

	// Check the most frequent CID
	expectedMostFrequentCid := snapshotCid
	for _, batchSubmission := range batchSubmissions {
		if batchSubmission.Batch.Cids[0] != expectedMostFrequentCid {
			t.Errorf("Expected CID %s, but got %s", expectedMostFrequentCid, batchSubmission.Batch.Cids[0])
		}
	}
}

func TestBuildBatchSubmissions_NoSubmissions(t *testing.T) {
	setup(t)

	config.SettingsObj.BatchSize = 10

	redis.RedisClient = redis.NewRedisClient()

	ipfs.ConnectIPFSNode()

	epochID := big.NewInt(1)
	header := "header1"

	batchSubmissions, err := BuildBatchSubmissions(epochID, []string{header})
	if err == nil {
		t.Errorf("Expected error for no submissions, but got none")
	}

	if len(batchSubmissions) != 0 {
		t.Errorf("Expected no batch submissions, but got some")
	}
}

func TestBuildBatchSubmissions_DifferentHeaders(t *testing.T) {
	setup(t)

	_, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	config.SettingsObj.BatchSize = 10

	redis.RedisClient = redis.NewRedisClient()

	ipfs.ConnectIPFSNode()

	epochID := big.NewInt(1)
	header1 := "header1"
	header2 := "header2"
	projectID := "project1"
	snapshotCid1 := "QmT5NvUtoM5nXMsCpS1dZXZeaZZ5iZZm7RM5czM11ik7x4"
	snapshotCid11 := "QmT5NvUtoM5nXMsCpS1dZXZeaZZ5iZZm7RM5czM11ik7x5"
	snapshotCid2 := "QmT5NvUtoM5nXMsCpS1dZXZeaZZ5iZZm7RM5czM11ik7x6"

	// Create and store mock submissions for header1
	submission1 := createMockSubmission(projectID, snapshotCid1)
	key1 := SubmissionKey(epochID.Uint64(), projectID, new(big.Int).SetUint64(submission1.Request.SlotId).String())
	value1 := fmt.Sprintf("%s.%s", uuid.New().String(), protojson.Format(submission1))
	set1 := redis.SubmissionSetByHeaderKey(epochID.Uint64(), header1)

	submission11 := createMockSubmission(projectID, snapshotCid11)
	key11 := SubmissionKey(epochID.Uint64(), projectID, new(big.Int).SetUint64(submission11.Request.SlotId).String())
	value11 := fmt.Sprintf("%s.%s", uuid.New().String(), protojson.Format(submission11))
	set11 := redis.SubmissionSetByHeaderKey(epochID.Uint64(), header1)

	submission12 := createMockSubmission(projectID, snapshotCid11)
	key12 := SubmissionKey(epochID.Uint64(), projectID, new(big.Int).SetUint64(submission12.Request.SlotId).String())
	value12 := fmt.Sprintf("%s.%s", uuid.New().String(), protojson.Format(submission12))
	set12 := redis.SubmissionSetByHeaderKey(epochID.Uint64(), header1)

	submission13 := createMockSubmission(projectID, snapshotCid11)
	key13 := SubmissionKey(epochID.Uint64(), projectID, new(big.Int).SetUint64(submission13.Request.SlotId).String())
	value13 := fmt.Sprintf("%s.%s", uuid.New().String(), protojson.Format(submission13))
	set13 := redis.SubmissionSetByHeaderKey(epochID.Uint64(), header1)

	// Create and store mock submissions for header2
	submission2 := createMockSubmission(projectID, snapshotCid2)
	key2 := SubmissionKey(epochID.Uint64(), projectID, new(big.Int).SetUint64(submission2.Request.SlotId).String())
	value2 := fmt.Sprintf("%s.%s", uuid.New().String(), protojson.Format(submission2))
	set2 := redis.SubmissionSetByHeaderKey(epochID.Uint64(), header2)

	if err := redis.SetSubmission(context.Background(), key1, value1, set1, 5*time.Minute); err != nil {
		t.Fatalf("Error setting mock submission1: %v", err)
	}

	if err := redis.SetSubmission(context.Background(), key11, value11, set11, 5*time.Minute); err != nil {
		t.Fatalf("Error setting mock submission1: %v", err)
	}

	if err := redis.SetSubmission(context.Background(), key12, value12, set12, 5*time.Minute); err != nil {
		t.Fatalf("Error setting mock submission1: %v", err)
	}

	if err := redis.SetSubmission(context.Background(), key13, value13, set13, 5*time.Minute); err != nil {
		t.Fatalf("Error setting mock submission1: %v", err)
	}

	if err := redis.SetSubmission(context.Background(), key2, value2, set2, 5*time.Minute); err != nil {
		t.Fatalf("Error setting mock submission2: %v", err)
	}

	batchSubmissions, err := BuildBatchSubmissions(epochID, []string{header1, header2})
	if err != nil {
		t.Errorf("Expected no error, but got: %v", err)
	}

	if len(batchSubmissions) == 0 {
		t.Errorf("Expected batch submissions, but got none")
	}

	for _, batchSubmission := range batchSubmissions {
		if batchSubmission.Batch.Cids[0] != snapshotCid11 {
			t.Errorf("Expected CID %s, but got %s", snapshotCid11, batchSubmission.Batch.Cids[0])
		}
		if batchSubmission.Batch.Pids[0] != projectID {
			t.Errorf("Expected project ID %s, but got %s", projectID, batchSubmission.Batch.Pids[0])
		}
	}
}

func TestBuildBatchSubmissions_InvalidKeys(t *testing.T) {
	setup(t)

	_, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	redis.RedisClient = redis.NewRedisClient()

	ipfs.ConnectIPFSNode()

	epochID := big.NewInt(1)
	header := "header1"
	invalidKey := "invalid.key.format"
	invalidValue := "some.invalid.value"

	if err := redis.SetSubmission(context.Background(), invalidKey, invalidValue, header, 5*time.Minute); err != nil {
		t.Fatalf("Error setting invalid mock submission: %v", err)
	}

	batchSubmissions, err := BuildBatchSubmissions(epochID, []string{header})
	if err == nil {
		t.Errorf("Expected error for invalid keys, but got none")
	}

	if len(batchSubmissions) != 0 {
		t.Errorf("Expected no batch submissions, but got some")
	}
}

func TestBuildBatchSubmissions_MixedProjectsToFillBatch(t *testing.T) {
	setup(t)

	_, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	redis.RedisClient = redis.NewRedisClient()

	ipfs.ConnectIPFSNode()

	epochID := big.NewInt(1)
	header := "header1"
	projectID1 := "project1"
	projectID2 := "project2"
	projectID3 := "project3"
	snapshotCid1 := "QmT5NvUtoM5nXMsCpS1dZXZeaZZ5iZZm7RM5czM11ik7x4"
	snapshotCid2 := "QmT5NvUtoM5nXMsCpS1dZXZeaZZ5iZZm7RM5czM11ik7x5"
	snapshotCid3 := "QmT5NvUtoM5nXMsCpS1dZXZeaZZ5iZZm7RM5czM11ik7x6"

	// Create and store mock submissions for project1
	for i := 0; i < 13; i++ {
		submission := createMockSubmission(projectID1, snapshotCid1)
		key := SubmissionKey(epochID.Uint64(), projectID1, new(big.Int).SetUint64(submission.Request.SlotId).String())
		value := fmt.Sprintf("%s.%s", uuid.New().String(), protojson.Format(submission))
		set := redis.SubmissionSetByHeaderKey(epochID.Uint64(), header)

		if err := redis.SetSubmission(context.Background(), key, value, set, 5*time.Minute); err != nil {
			t.Fatalf("Error setting mock submission: %v", err)
		}
	}

	// Create and store mock submissions for project2
	for i := 0; i < 6; i++ {
		submission := createMockSubmission(projectID2, snapshotCid2)
		key := SubmissionKey(epochID.Uint64(), projectID2, new(big.Int).SetUint64(submission.Request.SlotId).String())
		value := fmt.Sprintf("%s.%s", uuid.New().String(), protojson.Format(submission))
		set := redis.SubmissionSetByHeaderKey(epochID.Uint64(), header)

		if err := redis.SetSubmission(context.Background(), key, value, set, 5*time.Minute); err != nil {
			t.Fatalf("Error setting mock submission: %v", err)
		}
	}

	// Create and store mock submissions for project3
	for i := 0; i < 12; i++ {
		submission := createMockSubmission(projectID3, snapshotCid3)
		key := SubmissionKey(epochID.Uint64(), projectID3, new(big.Int).SetUint64(submission.Request.SlotId).String())
		value := fmt.Sprintf("%s.%s", uuid.New().String(), protojson.Format(submission))
		set := redis.SubmissionSetByHeaderKey(epochID.Uint64(), header)

		if err := redis.SetSubmission(context.Background(), key, value, set, 5*time.Minute); err != nil {
			t.Fatalf("Error setting mock submission: %v", err)
		}
	}

	batchSubmissions, err := BuildBatchSubmissions(epochID, []string{header})
	if err != nil {
		t.Errorf("Expected no error, but got: %v", err)
	}

	if len(batchSubmissions) == 0 {
		t.Errorf("Expected batch submissions, but got none")
	}

	// Check if the batch is filled with the expected number of submissions
	var totalSubmissions []int
	for _, batchSubmission := range batchSubmissions {
		totalSubmissions = append(totalSubmissions, len(batchSubmission.Batch.Submissions))
	}

	expectedTotalSubmissions := []int{19, 12}

	if totalSubmissions[0] != expectedTotalSubmissions[0] {
		t.Errorf("Expected %d total submissions in first batch, but got %d", expectedTotalSubmissions[0], totalSubmissions[0])
	}

	if totalSubmissions[1] != expectedTotalSubmissions[1] {
		t.Errorf("Expected %d total submissions in second batch, but got %d", expectedTotalSubmissions[1], totalSubmissions[1])
	}

	redisSubmissionKeys := []string{}

	redisSubmissionKeys, _ = redis.GetValidSubmissionKeys(context.Background(), epochID, []string{header})

	log.Debugln(len(redisSubmissionKeys))
	// map of all submission keys and values in redis for the given epoch and header string to string[]
	redisSubmissionCids := make(map[string]string)

	for _, key := range redisSubmissionKeys {
		val, err := redis.Get(context.Background(), key)
		if err != nil {
			log.Errorln("Error getting value from redis: ", err.Error())
		}
		redisSubmissionCids[key] = utils.ExtractField(val, "snapshotCid")
	}

	redisSubmissionCidsSorted := make(map[string][]string)

	for key, value := range redisSubmissionCids {
		// as the key format is in the form of projectID.slotID, we can split the key by "." to get the projectID
		projectID := strings.Split(key, ".")[0]
		redisSubmissionCidsSorted[projectID] = append(redisSubmissionCidsSorted[projectID], value)
	}

	// Extract and count the CIDs from the batch submissions
	actualCIDFrequency := make(map[string]int)
	for _, batchSubmission := range batchSubmissions {
		for _, submission := range batchSubmission.Batch.Submissions {
			cid := utils.ExtractField(submission, "snapshotCid")
			actualCIDFrequency[cid]++
		}
	}

	// Verify that the most frequent CID are included in the batch submissions with the correct frequency
	for projectID, expectedCids := range redisSubmissionCidsSorted {
		for _, expectedCid := range expectedCids {
			actualCount, found := actualCIDFrequency[expectedCid]
			if !found {
				t.Errorf("Expected CID %s for project %s to be found in batch submissions, but it was not found", expectedCid, projectID)
			} else if actualCount != len(expectedCids) {
				t.Errorf("Expected CID %s for project %s to have frequency %d, but got %d", expectedCid, projectID, len(expectedCids), actualCount)
			}
		}
	}
}

func TestBuildBatchSubmissions_Fixture(t *testing.T) {
	setup(t)
	ctx := context.Background()

	config.SettingsObj.BatchSize = 10

	ipfs.ConnectIPFSNode()

	var submissions []SubmissionDetails

	// Open the file containing the keys
	file, err := os.Open("./fixtures/submissionKeys.txt")
	if err != nil {
		log.Fatalf("failed to open file: %v", err)
	}
	defer file.Close()

	// Read the file line by line
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key := scanner.Text()

		// Split the key by the "." delimiter
		parts := strings.Split(key, ".")

		// Ensure there are exactly three parts
		if len(parts) != 3 {
			log.Printf("unexpected format for key: %s", key)
			continue
		}

		epochId, err := strconv.ParseUint(parts[0], 10, 64)
		if err != nil {
			log.Printf("invalid epochId: %s", parts[0])
			continue
		}

		slotId, err := strconv.ParseUint(parts[2], 10, 64)
		if err != nil {
			log.Printf("invalid slotId: %s", parts[2])
			continue
		}

		// Generate a submission for each key
		submission := SubmissionDetails{
			day:          big.NewInt(1),
			submissionId: uuid.New(),
			submission: &pkgs.SnapshotSubmission{
				Request: &pkgs.Request{
					SlotId:      slotId,
					Deadline:    uint64(rand.Int63()),
					SnapshotCid: "QmT5NvUtoM5nXMsCpS1dZXZeaZZ5iZZm7RM5czM11ik7x4",
					EpochId:     epochId,
					ProjectId:   parts[1],
				},
				Signature: "signature" + uuid.New().String(),
				Header:    "header1",
			},
		}
		submissions = append(submissions, submission)
	}

	// Set submissions in Redis
	for _, submission := range submissions {
		key := SubmissionKey(submission.submission.Request.EpochId, submission.submission.Request.ProjectId, new(big.Int).SetUint64(submission.submission.Request.SlotId).String())
		value := fmt.Sprintf("%s.%s", submission.submissionId.String(), protojson.Format(submission.submission))
		set := redis.SubmissionSetByHeaderKey(submission.submission.Request.EpochId, submission.submission.Header)

		if err := redis.SetSubmission(ctx, key, value, set, 5*time.Minute); err != nil {
			log.Errorln("Error setting key-value pair: ", err.Error())
		}
	}

	// Build batch submissions
	batchSubmissions, err := BuildBatchSubmissions(big.NewInt(5088), []string{"header1"})
	if err != nil {
		t.Errorf("Expected no error, but got: %v", err)
	}

	t.Log("Batch submissions length: ", len(batchSubmissions))

	// Check the returned value
	if batchSubmissions == nil {
		t.Errorf("Expected batchSubmissions to be not nil")
	}
}

func createMockSubmission(projectID, snapshotCid string) *pkgs.SnapshotSubmission {
	return &pkgs.SnapshotSubmission{
		Request: &pkgs.Request{
			SlotId:      uint64(rand.Int63()),
			Deadline:    uint64(rand.Int63()),
			SnapshotCid: snapshotCid,
			EpochId:     1,
			ProjectId:   projectID,
		},
		Signature: "signature" + uuid.New().String(),
		Header:    "header1",
	}
}

func SubmissionKey(epochId uint64, projectId, slotId string) string {
	return fmt.Sprintf("%d.%s.%s", epochId, projectId, slotId)
}
