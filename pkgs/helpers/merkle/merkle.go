package merkle

import (
	"collector/config"
	"collector/pkgs"
	"collector/pkgs/helpers/clients"
	"collector/pkgs/helpers/ipfs"
	"collector/pkgs/helpers/redis"
	"context"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/sergerad/incremental-merkle-tree/imt"
	log "github.com/sirupsen/logrus"
	"google.golang.org/protobuf/encoding/protojson"
)

var BatchId int

func UpdateMerkleTree(sortedData []string, tree *imt.IncrementalMerkleTree) (*imt.IncrementalMerkleTree, error) {
	for _, value := range sortedData {
		err := tree.AddLeaf([]byte(value))
		if err != nil {
			log.Errorf("Error adding merkle tree leaf: %s\n", err.Error())
			return nil, err
		}
	}

	return tree, nil
}

func GetRootHash(tree *imt.IncrementalMerkleTree) string {
	return common.Bytes2Hex(tree.RootDigest())
}

func BuildBatchSubmissions(epochId *big.Int, headers []string) ([]*ipfs.BatchSubmission, error) {
	keys, err := redis.GetValidSubmissionKeys(context.Background(), epochId, headers)
	log.Debugln("Fetched keys: ", len(keys))
	if err != nil {
		return nil, err
	}
	log.Debugf("Fetched %d keys for epoch %d", len(keys), epochId)

	if len(keys) == 0 {
		log.Debugf("no submissions for epoch: %s, with headers: %s", epochId.String(), headers)
		return []*ipfs.BatchSubmission{}, errors.New("no submissions for epoch")
	}

	sort.Strings(keys)

	tree, err := imt.New()
	if err != nil {
		clients.SendFailureNotification("BuildBatchSubmissions", fmt.Sprintf("Error creating submissions ID merkle tree: %s\n", err.Error()), time.Now().String(), "High")
		log.Errorf("Error creating submissions ID merkle tree: %s\n", err.Error())
		return nil, err
	}

	batchedKeys := arrangeKeysInBatches(keys)

	log.Debugln("Arranged keys in batches: ")
	log.Debugln(batchedKeys)

	batchSubmissions, err := finalizeBatches(batchedKeys, epochId, tree)

	if err != nil {
		clients.SendFailureNotification("BuildBatchSubmissions", fmt.Sprintf("Batch finalization error: %s", err.Error()), time.Now().String(), "High")
		log.Errorln("Batch finalization error: ", err.Error())
	}

	log.Debugf("Finalized batch submissions for epoch %d: \n", epochId)

	return batchSubmissions, err
}

func finalizeBatches(batchedKeys [][]string, epochId *big.Int, tree *imt.IncrementalMerkleTree) ([]*ipfs.BatchSubmission, error) {
	//projectValueFrequencies := make(map[string]map[string]int)
	//projectMostFrequent := make(map[string]string)
	batchSubmissions := make([]*ipfs.BatchSubmission, 0)
	//TODO: Don't just return the most frequent, if it is not 51% consensus, trigger a signal for watchers

	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, batch := range batchedKeys {
		wg.Add(1)

		go func(batch []string) {
			defer wg.Done()

			log.Debugln("Processing batch: ", batch)
			allIds := []string{}
			allData := []string{}
			localProjectMostFrequent := make(map[string]string)
			localProjectValueFrequencies := make(map[string]map[string]int)

			for _, key := range batch {
				val, err := redis.Get(context.Background(), key)  // submission data is uuid.submission_json

				if err != nil {
					clients.SendFailureNotification("finalizeBatches", fmt.Sprintf("Error fetching data from redis: %s", err.Error()), time.Now().String(), "High")
					log.Errorln("Error fetching data from redis: ", err.Error())
					continue
				}

				log.Debugln(fmt.Sprintf("Processing key %s and value %s", key, val))

			if len(val) == 0 {
				clients.SendFailureNotification("finalizeBatches", fmt.Sprintf("Value has expired for key, not being counted in batch: %s", key), time.Now().String(), "High")
				log.Errorln("Value has expired for key:  ", key)
				continue
			}

				parts := strings.Split(key, ".")
				if len(parts) != 3 {
					clients.SendFailureNotification("finalizeBatches", fmt.Sprintf("Key should have three parts, invalid key: %s", key), time.Now().String(), "High")
					log.Errorln("Key should have three parts, invalid key: ", key)
					continue // skip malformed keys
				}
				projectId := parts[1]

				if localProjectValueFrequencies[projectId] == nil {
					localProjectValueFrequencies[projectId] = make(map[string]int)
				}

				idSubPair := strings.Split(val, ".")
				if len(idSubPair) != 2 {
					clients.SendFailureNotification("finalizeBatches", fmt.Sprintf("Value should have two parts, invalid value: %s", val), time.Now().String(), "High")
					log.Errorln("Value should have two parts, invalid value: ", val)
					continue // skip malformed keys
				}

				subHolder := pkgs.SnapshotSubmission{}
				err = protojson.Unmarshal([]byte(idSubPair[1]), &subHolder)
				if err != nil {
					clients.SendFailureNotification("finalizeBatches", fmt.Sprintf("Unmarshalling %s error: %s", idSubPair[1], err.Error()), time.Now().String(), "High")
					log.Errorln("Unable to unmarshal submission: ", err)
					continue
				}

				value := subHolder.Request.SnapshotCid

				localProjectValueFrequencies[projectId][value] += 1

				if count, exists := localProjectValueFrequencies[projectId][value]; exists {
					if count > localProjectValueFrequencies[projectId][localProjectMostFrequent[projectId]] {
						localProjectMostFrequent[projectId] = value
					}
				}

				allData = append(allData, idSubPair[1])
				allIds = append(allIds, idSubPair[0])
			}

			var keys []string
			for pid := range localProjectMostFrequent {
				keys = append(keys, pid)
			}

			pids := []string{}
			cids := []string{}
			sort.Strings(keys)
			for _, pid := range keys {
				pids = append(pids, pid)
				cids = append(cids, localProjectMostFrequent[pid]) // finalized CIDs
			}

			log.Debugln("PIDs and CIDs for epoch: ", epochId, pids, cids)

			batchSubmission, err := BuildBatch(allIds, allData, BatchId, epochId, tree, pids, cids)
			if err != nil {
				clients.SendFailureNotification("finalizeBatches", fmt.Sprintf("Batch building error: %s", err.Error()), time.Now().String(), "High")
				log.Errorln("Error storing the batch: ", err.Error())
				return
			}

			mu.Lock()
			batchSubmissions = append(batchSubmissions, batchSubmission)
			BatchId++
			mu.Unlock()

			log.Debugf("CID: %s Batch: %d", batchSubmission.Cid, BatchId-1)
		}(batch)
	}

	wg.Wait()
	ids := []string{}
	for _, bs := range batchSubmissions {
		ids = append(ids, bs.Batch.ID.String())
	}

	// Set finalized batches in redis for epochId
	logEntry := map[string]interface{}{
		"epoch_id":                epochId.String(),
		"finalized_batches_count": len(batchSubmissions),
		"finalized_batch_ids":     ids,
		"timestamp":               time.Now().Unix(),
	}

	if err := redis.SetProcessLog(context.Background(), redis.TriggeredProcessLog(pkgs.FinalizeBatches, epochId.String()), logEntry, 4*time.Hour); err != nil {
		clients.SendFailureNotification("finalizeBatches", err.Error(), time.Now().String(), "High")
		log.Errorln("finalizeBatches process log error: ", err.Error())
	}

	return batchSubmissions, nil
}

func arrangeKeysInBatches(keys []string) [][]string {
	projectMap := make(map[string][]string)
	for _, key := range keys {
		parts := strings.Split(key, ".")
		if len(parts) != 3 {
			log.Errorln("Improper key stored in redis: ", key)
			clients.SendFailureNotification("arrangeKeysInBatches", fmt.Sprintf("Improper key stored in redis: %s", key), time.Now().String(), "High")
			continue // skip malformed entries
		}
		projectId := parts[1]
		projectMap[projectId] = append(projectMap[projectId], key)
	}

	var batches [][]string
	currentBatch := make([]string, 0, config.SettingsObj.BatchSize)

	for _, projectKeys := range projectMap {
		// Check if the current batch can accommodate all project keys
		if len(currentBatch)+len(projectKeys) <= config.SettingsObj.BatchSize {
			currentBatch = append(currentBatch, projectKeys...)
		} else {
			if len(currentBatch) > 0 {
				batches = append(batches, currentBatch)
			}
			currentBatch = projectKeys
		}
		// If current batch reaches batch size, push to batches and reset current batch
		if len(currentBatch) >= config.SettingsObj.BatchSize {
			batches = append(batches, currentBatch)
			currentBatch = make([]string, 0, config.SettingsObj.BatchSize)
		}
	}

	// Append the last batch if it has any keys
	if len(currentBatch) > 0 {
		batches = append(batches, currentBatch)
	}

	return batches
}

func BuildBatch(dataIds, data []string, id int, epochId *big.Int, tree *imt.IncrementalMerkleTree, pids, cids []string) (*ipfs.BatchSubmission, error) {
	log.Debugln("Building batch for epoch: ", epochId.String())
	var err error
	_, err = UpdateMerkleTree(dataIds, tree)
	if err != nil {
		return nil, err
	}
	roothash := GetRootHash(tree)
	log.Debugln("RootHash for batch ", id, roothash)
	batch := &ipfs.Batch{ID: big.NewInt(int64(id)), SubmissionIds: dataIds, Submissions: data, RootHash: roothash, Pids: pids, Cids: cids}
	if cid, err := ipfs.StoreOnIPFS(ipfs.IPFSCon, batch); err != nil {
		clients.SendFailureNotification("Build Batch", fmt.Sprintf("Error storing batch %d on IPFS: %s", id, err.Error()), time.Now().String(), "High")
		log.Errorf("Error storing batch on IPFS: %d", id)
		return nil, err
	} else {
		log.Debugln("Stored cid for batch ", id, cid)
		// Set batch building success for epochId
		logEntry := map[string]interface{}{
			"epoch_id":          epochId.String(),
			"batch_id":          id,
			"batch_cid":         cid,
			"submissions_count": len(data),
			"submissions":       data,
			"timestamp":         time.Now().Unix(),
		}

		if err = redis.SetProcessLog(context.Background(), redis.TriggeredProcessLog(pkgs.BuildBatch, strconv.Itoa(id)), logEntry, 4*time.Hour); err != nil {
			clients.SendFailureNotification("BuildBatch", err.Error(), time.Now().String(), "High")
			log.Errorln("BuildBatch process log error: ", err.Error())
		}

		cidTree, _ := imt.New()
		if _, err := UpdateMerkleTree(batch.Cids, cidTree); err != nil {
			clients.SendFailureNotification("Build Batch", fmt.Sprintf("Error updating merkle tree for batch %d: %s", id, err.Error()), time.Now().String(), "High")
			log.Errorln("Unable to get finalized root hash: ", err.Error())
			return nil, err
		}
		return &ipfs.BatchSubmission{
			Batch:                 batch,
			Cid:                   cid,
			EpochId:               epochId,
			FinalizedCidsRootHash: cidTree.RootDigest(),
		}, nil
	}
}
