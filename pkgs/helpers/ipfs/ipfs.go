package ipfs

import (
	"bytes"
	"collector/config"
	"encoding/json"
	"github.com/ipfs/go-ipfs-api"
	log "github.com/sirupsen/logrus"
	"math/big"
)

var IPFSCon *shell.Shell

// Batch represents your data structure
type Batch struct {
	ID            *big.Int `json:"id"`
	SubmissionIds []string `json:"submissionIds"`
	Submissions   []string `json:"submissions"`
	RootHash      string   `json:"roothash"`
	Pids          []string `json:"pids"`
	Cids          []string `json:"cids"`
}

type BatchSubmission struct {
	Batch                 *Batch
	Cid                   string
	EpochId               *big.Int
	FinalizedCidsRootHash []byte
}

// Connect to the local IPFS node
func ConnectIPFSNode() {
	log.Debugf("Connecting to IPFS host: %s", config.SettingsObj.IPFSUrl)
	IPFSCon = shell.NewShell(config.SettingsObj.IPFSUrl)
}

func StoreOnIPFS(sh *shell.Shell, data *Batch) (string, error) {
	jsonData, err := json.Marshal(data)
	cid, err := sh.Add(bytes.NewReader(jsonData))
	if err != nil {
		return "", err
	}
	return cid, nil
}
