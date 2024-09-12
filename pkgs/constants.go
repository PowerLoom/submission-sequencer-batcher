package pkgs

import "time"

// Process Name Constants
// process : identifier
const (
	TriggerCollectionFlow        = "trigger_collection_flow"
	FinalizeBatches              = "finalize_batch_submissions"
	CommitSubmissionBatch        = "commit_submission_batch"
	UpdateRewards                = "update_rewards"
	BuildBatch                   = "build_batch"
	EnsureBatchSubmissionSuccess = "batch_resubmission"
)

// General Key Constants
const (
	MaxBatchRetries             = 3
	MaxRewardUpdateRetries      = 5
	RewardsBackendFailures      = "RewardsBackendFailures"
	CurrentEpoch                = "CurrentEpochID"
	TransactionReceiptCount     = "TransactionReceiptCount"
	SequencerCurrentBlockNumber = "SequencerState.CurrentBlockNumber"
	CollectorKey                = "SnapshotCollector"
	TxsKey                      = "SnapshotTransactions"
	SlotSubmissionsKey          = "SlotSubmissionsKey"
	DaySubmissionsKey           = "DaySubmissionsKey"
	Day                         = 24 * time.Hour
	RewardTxKey                 = "RewardTx"
	DayBuffer                   = 0
	EpochsPerDay                = 3
	DailyRewardPointsKey        = "DailyRewardPointsKey"
	TotalRewardPointsKey        = "TotalRewardPointsKey"
	TaskCompletionKey           = "TaskCompletionKey"
	SlotStreakKey               = "SlotStreakKey"
	EpochSubmissionsCountKey    = "EpochSubmissionsCountKey"
	EpochSubmissionsKey         = "EpochSubmissionsKey"
	ProcessTriggerKey           = "TriggeredSequencerProcess"
	SequencerDayKey             = "SequencerState.Day"
	EPOCH_SIZE                  = "ProtocolState.EPOCH_SIZE"
	SOURCE_CHAIN_BLOCK_TIME     = "ProtocolState.SOURCE_CHAIN_BLOCK_TIME"
	ReceiptProcessedKey         = "ReceiptProcessedKey"
)
