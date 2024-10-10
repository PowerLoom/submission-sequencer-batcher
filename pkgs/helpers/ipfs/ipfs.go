package ipfs

import (
	"bytes"
	"collector/config"
	"crypto/tls"
	"encoding/json"
	"github.com/cenkalti/backoff/v4"
	"github.com/ipfs/go-ipfs-api"
	log "github.com/sirupsen/logrus"
	"math/big"
	"net/http"
	"time"
)

var IPFSCon *shell.Shell

// Batch represents your data structure
type Batch struct {
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
	IPFSCon = shell.NewShellWithClient(
		config.SettingsObj.IPFSUrl,
		&http.Client{
			Timeout: time.Duration(config.SettingsObj.HttpTimeout) * time.Second,
			Transport: &http.Transport{
				TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
				MaxIdleConns:      10,
				IdleConnTimeout:   5 * time.Second,
				DisableKeepAlives: true,
			},
		},
	)
}

func StoreOnIPFS(sh *shell.Shell, data *Batch) (string, error) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return "", err
	}

	cid := ""

	err = backoff.Retry(
		func() error {
			cid, err = sh.Add(bytes.NewReader(jsonData))
			return err
		},
		backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 3))

	if err != nil {
		return "", err
	}

	return cid, nil
}
