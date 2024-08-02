package prost

import (
	"collector/pkgs/helpers/redis"
	"collector/pkgs/helpers/utils"
	"context"
	"github.com/alicebob/miniredis/v2"
	redisv8 "github.com/go-redis/redis/v8"
	"math/big"
	"testing"
)

func setupRedis() {
	// Create a mini Redis server for testing
	s, err := miniredis.Run()
	if err != nil {
		panic(err)
	}

	redis.RedisClient = redisv8.NewClient(&redisv8.Options{
		Addr: s.Addr(),
	})
	utils.InitLogger()
}

func TestCalculateAndStoreRewards(t *testing.T) {
	setupRedis()

	RewardBasePoints = big.NewInt(100)
	DailySnapshotQuota = big.NewInt(50)

	tests := []struct {
		name                 string
		malleate             func() // Used to set constants and redis as per test requirements
		day                  int64
		slotId               int64
		firstDay             bool
		expectedDailyRewards int64
		expectedTotalRewards int64
	}{
		{
			name: "Day 1, initial state without streak bonus",
			malleate: func() {
			},
			day:                  1,
			slotId:               4699,
			firstDay:             false,
			expectedDailyRewards: 100,
			expectedTotalRewards: 100,
		},
		{
			name: "Day 2, previous day completed",
			malleate: func() {
				CalculateAndStoreRewards(big.NewInt(1), big.NewInt(4699))
			},
			day:                  2,
			slotId:               4699,
			firstDay:             false,
			expectedDailyRewards: 100,
			expectedTotalRewards: 200,
		},
		{
			name: "Day 2, previous day not completed",
			malleate: func() {
			},
			day:                  2,
			slotId:               4699,
			firstDay:             false,
			expectedDailyRewards: 100,
			expectedTotalRewards: 100,
		},
		{
			name: "Day 3, task completed on day 1",
			malleate: func() {
				CalculateAndStoreRewards(big.NewInt(1), big.NewInt(4699))
			},
			day:                  3,
			slotId:               4699,
			firstDay:             false,
			expectedDailyRewards: 100,
			expectedTotalRewards: 200,
		},
		{
			name: "Day 3, task completed on day 2",
			malleate: func() {
				CalculateAndStoreRewards(big.NewInt(2), big.NewInt(4699))
			},
			day:                  3,
			slotId:               4699,
			firstDay:             false,
			expectedDailyRewards: 100,
			expectedTotalRewards: 200,
		},
		{
			name: "Day 3, task completed on day 1 and 2",
			malleate: func() {
				CalculateAndStoreRewards(big.NewInt(1), big.NewInt(4699))
				CalculateAndStoreRewards(big.NewInt(2), big.NewInt(4699))
			},
			day:                  3,
			slotId:               4699,
			firstDay:             false,
			expectedDailyRewards: 100,
			expectedTotalRewards: 300,
		},
		{
			name: "Day 9, task completed on days: {1, 2, 5, 6, 7}",
			malleate: func() {
				CalculateAndStoreRewards(big.NewInt(1), big.NewInt(4699))
				CalculateAndStoreRewards(big.NewInt(2), big.NewInt(4699))
				CalculateAndStoreRewards(big.NewInt(5), big.NewInt(4699))
				CalculateAndStoreRewards(big.NewInt(6), big.NewInt(4699))
				CalculateAndStoreRewards(big.NewInt(7), big.NewInt(4699))
			},
			day:                  3,
			slotId:               4699,
			firstDay:             false,
			expectedDailyRewards: 100,
			expectedTotalRewards: 600,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			day := big.NewInt(tt.day)
			slotId := big.NewInt(tt.slotId)

			redis.Set(context.Background(), redis.TotalSlotRewards("4699"), "0", 0)

			tt.malleate()

			CalculateAndStoreRewards(day, slotId)

			// Verify rewards in Redis
			rewardsStr, err := redis.Get(context.Background(), redis.SlotRewardsForDay(day.String(), slotId.String()))
			if err != nil {
				t.Fatalf("Failed to get rewards from redis: %v", err)
			}
			rewards, _ := new(big.Int).SetString(rewardsStr, 10)
			if rewards.Int64() != tt.expectedDailyRewards {
				t.Errorf("Expected rewards %d, got %d", tt.expectedDailyRewards, rewards.Int64())
			}

			rewardsStr, err = redis.Get(context.Background(), redis.TotalSlotRewards(slotId.String()))
			if err != nil {
				t.Fatalf("Failed to get rewards from redis: %v", err)
			}
			rewards, _ = new(big.Int).SetString(rewardsStr, 10)
			if rewards.Int64() != tt.expectedTotalRewards {
				t.Errorf("Expected total rewards %d, got %d", tt.expectedTotalRewards, rewards.Int64())
			}
			redis.RedisClient.FlushDB(context.Background()).Val()
		})
	}
}
