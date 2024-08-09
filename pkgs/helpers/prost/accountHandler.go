package prost

import (
	"collector/config"
	"collector/pkgs/helpers/clients"
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	log "github.com/sirupsen/logrus"
	"math/big"
	"strings"
	"sync"
	"time"
)

var ProcessesOnHold = 0

type Account struct {
	auth       *bind.TransactOpts
	mu         sync.Mutex
	address    common.Address
	privateKey *ecdsa.PrivateKey
}

type AccountHandler struct {
	accounts []*Account
	mu       sync.Mutex
	cond     *sync.Cond
}

func NewAccountHandler() *AccountHandler {
	mutex := sync.Mutex{}
	syncCond := sync.NewCond(&mutex)
	accHandler := &AccountHandler{mu: sync.Mutex{}, cond: syncCond}
	accHandler.initializeAccounts()
	if len(accHandler.accounts) == 0 {
		log.Fatalf("No signers configured")
	}
	return accHandler
}

// InitializeAccounts TODO: This should decode the address from the private key to catch errors due to order mismatch
func (ah *AccountHandler) initializeAccounts() {
	for i, addr := range config.SettingsObj.SignerAccountAddresses {
		pk, err := crypto.HexToECDSA(config.SettingsObj.PrivateKeys[i])
		if err != nil {
			log.Fatalf("Failed to parse private key: %v", err)
		}

		account := &Account{address: common.HexToAddress(addr), privateKey: pk, mu: sync.Mutex{}}
		ah.accounts = append(ah.accounts, account)
	}
}

func (account *Account) SetupAuth() error {
	nonce, err := Client.PendingNonceAt(context.Background(), account.address)
	if err != nil {
		return errors.New("failed to get nonce: " + err.Error())
	}

	auth, err := bind.NewKeyedTransactorWithChainID(account.privateKey, big.NewInt(config.SettingsObj.ChainID))
	if err != nil {
		return errors.New("failed to create authorized transactor: " + err.Error())
	}

	auth.Nonce = new(big.Int).SetUint64(nonce)
	auth.Value = big.NewInt(0) // in wei
	auth.GasLimit = defaultGasLimit
	auth.From = account.address
	account.auth = auth

	if err = account.UpdateGasPrice(context.Background(), 1); err != nil {
		log.Errorln(err.Error())
		auth.GasPrice = new(big.Int).SetUint64(defaultGasLimit)
	}

	return nil
}

func (account *Account) UpdateGasPrice(ctx context.Context, multiplier int) (err error) {
	account.auth.GasFeeCap, account.auth.GasTipCap, err = SuggestEIP1559Fees(ctx, int64(multiplier))
	return err
}

// SuggestEIP1559Fees suggests fees for EIP-1559 transactions.
func SuggestEIP1559Fees(ctx context.Context, multiplier int64) (maxFeePerGas, maxPriorityFeePerGas *big.Int, err error) {
	// Suggest a gas tip (priority fee) using the eth_maxPriorityFeePerGas method
	tip, err := Client.SuggestGasTipCap(ctx)
	if err != nil {
		return nil, nil, err
	}

	// Retrieve the base fee for the next block
	head := CurrentBlock.Header()
	baseFee := head.BaseFee

	// Calculate maxFeePerGas: baseFee * 2 + priorityFee
	maxFeePerGas = new(big.Int).Mul(baseFee, big.NewInt(multiplier))
	maxFeePerGas.Add(maxFeePerGas, new(big.Int).Mul(tip, big.NewInt(multiplier)))

	return maxFeePerGas, tip, nil
}

func (account *Account) UpdateAuth(num uint64) {
	account.auth.Nonce = new(big.Int).Add(account.auth.Nonce, new(big.Int).SetUint64(num))
}

func (ah *AccountHandler) GetFreeAccount(canWait bool) *Account {
	ah.mu.Lock()
	defer ah.mu.Unlock()
	for {
		for _, account := range ah.accounts {
			if account.mu.TryLock() {
				account.SetupAuth()
				log.Debugln("Locking account: ", account.address.Hex())
				if ProcessesOnHold > 0 {
					ProcessesOnHold -= 1
				}
				return account
			}
		}
		log.Debugln("All accounts are occupied - waiting")
		if !canWait {
			return nil
		}
		ProcessesOnHold += 1
		if ProcessesOnHold > 2 {
			log.Errorf("%d Processes on hold: all accounts are occupied", ProcessesOnHold)
			clients.SendFailureNotification("GetFreeAccount", fmt.Sprintf("%d Processes on hold: all accounts are occupied", ProcessesOnHold), time.Now().String(), "High")
		}
		ah.cond.L.Lock()
		ah.cond.Wait()
		ah.cond.L.Unlock()
	}
}

func (ah *AccountHandler) ReleaseAccount(account *Account) {
	log.Debugln("Releasing account: ", account.address.Hex())
	account.mu.Unlock()
	ah.cond.Signal()
}

func (account *Account) HandleTransactionError(err error, multiplier int, id string) int {
	log.Debugf("Found error: %s proceeding with adjustments for tx %s\n", err.Error(), id)
	if strings.Contains(err.Error(), "nonce too low") || strings.Contains(err.Error(), "nonce too high") {
		log.Errorf("Nonce mismatch for %s, error: %s\n", id, err.Error())
		nonce, ok := extractNonceFromError(err.Error())
		var diff uint64
		if ok {
			log.Debugln("Extracted nonce from err: ", nonce)
			diff = nonce - account.auth.Nonce.Uint64()
		} else {
			log.Errorf("Unable to extract nonce from error")
			pending, err := Client.PendingNonceAt(context.Background(), account.auth.From)
			if err != nil {
				log.Debugln("Client failure: ", err.Error())
				return multiplier
			}
			diff = pending - account.auth.Nonce.Uint64()
		}
		account.UpdateAuth(diff)
		log.Debugln("Retrying with nonce: ", account.auth.Nonce.String())
	} else if strings.Contains(err.Error(), "transaction underpriced") {
		log.Errorf("Transaction %s underpriced error: %s\n", id, err.Error())
		multiplier++
		account.UpdateGasPrice(context.Background(), multiplier)
		log.Debugln("Retrying with gas price: ", account.auth.GasFeeCap.String())
	} else {
		log.Errorf("Unexpected error: %v", err)
		clients.SendFailureNotification("HandleTransactionError", err.Error(), time.Now().String(), "High")
	}
	return multiplier
}
