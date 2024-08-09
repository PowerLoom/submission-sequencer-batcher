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
	RewardsBackendFailures      = "RewardsBackendFailures"
	CurrentEpoch                = "CurrentEpochID"
	TransactionReceiptCount     = "TransactionReceiptCount"
	SequencerCurrentBlockNumber = "SequencerState.CurrentBlockNumber"
	CollectorKey                = "SnapshotCollector"
	TxsKey                      = "SnapshotTransactions"
	TimeSlotKey                 = "TimeSlotPreference"
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
	DayCounterKey               = "DayCounterKey"
	EpochSubmissionsCountKey    = "EpochSubmissionsCountKey"
	EpochSubmissionsKey         = "EpochSubmissionsKey"
	ProcessTriggerKey           = "TriggeredSequencerProcess"
	SequencerDayKey             = "SequencerState.Day"
)
