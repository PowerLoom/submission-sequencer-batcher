package redis

import (
	"collector/pkgs"
	"fmt"
)

func SubmissionSetByHeaderKey(epoch uint64, header string) string {
	return fmt.Sprintf("%s.%d.%s", pkgs.CollectorKey, epoch, header)
}

func TriggeredProcessLog(process, identifier string) string {
	return fmt.Sprintf("%s.%s.%s", pkgs.ProcessTriggerKey, process, identifier)
}
func EpochSubmissionsCount(epochId uint64) string {
	return fmt.Sprintf("%s.%d", pkgs.EpochSubmissionsCountKey, epochId)
}

func EpochSubmissionsKey(epochId uint64) string {
	return fmt.Sprintf("%s.%d", pkgs.EpochSubmissionsKey, epochId)
}

func BatchSubmissionSetByEpoch(epochId string) string {
	return fmt.Sprintf("%s.%s", pkgs.TxsKey, epochId)
}

func BatchSubmissionKey(batchId string, nonce string) string {
	return fmt.Sprintf("%s.%s", batchId, nonce)
}

func RewardUpdateSetByDay(day string) string {
	return fmt.Sprintf("%s.%s", pkgs.RewardTxKey, day)
}

func RewardTxSlots(tx string) string {
	return fmt.Sprintf("%s.%s", tx, "Slots")
}

func RewardTxSubmissions(tx string) string {
	return fmt.Sprintf("%s.%s", tx, "Submissions")
}

func SlotSubmissionSetByDay(day string) string {
	return fmt.Sprintf("%s.%s", pkgs.DaySubmissionsKey, day)
}

func SlotSubmissionKey(slotId, day string) string {
	return fmt.Sprintf("%s.%s.%s", pkgs.SlotSubmissionsKey, day, slotId)
}

func SlotRewardsForDay(day string, slotId string) string {
	return fmt.Sprintf("%s.%s.%s", pkgs.DailyRewardPointsKey, day, slotId)
}

func TotalSlotRewards(slotId string) string {
	return fmt.Sprintf("%s.%s", pkgs.TotalRewardPointsKey, slotId)
}

func LatestDailyTaskCompletion(slotId string) string {
	return fmt.Sprintf("%s.%s", pkgs.TaskCompletionKey, slotId)
}

func SlotStreakCounter(slotId string) string {
	return fmt.Sprintf("%s.%s", pkgs.SlotStreakKey, slotId)
}
