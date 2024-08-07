package redis

import (
	"collector/config"
	"collector/pkgs/helpers/clients"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-redis/redis/v8"
	log "github.com/sirupsen/logrus"
	"math/big"
	"strconv"
	"time"
)

var RedisClient *redis.Client

// TODO: Pool size failures to be checked
func NewRedisClient() *redis.Client {
	db, err := strconv.Atoi(config.SettingsObj.RedisDB)
	if err != nil {
		log.Fatalf("Incorrect redis db: %s", err.Error())
	}
	return redis.NewClient(&redis.Options{
		Addr:         fmt.Sprintf("%s:%s", config.SettingsObj.RedisHost, config.SettingsObj.RedisPort), // Redis server address
		Password:     "",                                                                               // no password set
		DB:           db,
		PoolSize:     1000,
		ReadTimeout:  200 * time.Millisecond,
		WriteTimeout: 200 * time.Millisecond,
		DialTimeout:  5 * time.Second,
		IdleTimeout:  5 * time.Minute,
	})
}

func AddToSet(ctx context.Context, set string, keys ...string) error {
	if err := RedisClient.SAdd(ctx, set, keys).Err(); err != nil {
		clients.SendFailureNotification("Redis error", err.Error(), time.Now().String(), "High")
		return err
	}
	return nil
}

func GetSetKeys(ctx context.Context, set string) []string {
	return RedisClient.SMembers(ctx, set).Val()
}

func RemoveFromSet(ctx context.Context, set, key string) error {
	return RedisClient.SRem(context.Background(), set, key).Err()
}

func Delete(ctx context.Context, set string) error {
	return RedisClient.Del(ctx, set).Err()
}

func SetSubmission(ctx context.Context, key string, value string, set string, expiration time.Duration) error {
	if err := RedisClient.SAdd(ctx, set, key).Err(); err != nil {
		clients.SendFailureNotification("Redis error", err.Error(), time.Now().String(), "High")
		return err
	}
	if err := RedisClient.Set(ctx, key, value, expiration).Err(); err != nil {
		clients.SendFailureNotification("Redis error", err.Error(), time.Now().String(), "High")
		return err
	}
	return nil
}

func Get(ctx context.Context, key string) (string, error) {
	val, err := RedisClient.Get(ctx, key).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return "", nil
		} else {
			clients.SendFailureNotification("Redis error", err.Error(), time.Now().String(), "High")
			return "", err
		}
	}
	return val, nil
}

func Set(ctx context.Context, key, value string, expiration time.Duration) error {
	return RedisClient.Set(ctx, key, value, expiration).Err()
}

// Save log to Redis
func SetProcessLog(ctx context.Context, key string, logEntry map[string]interface{}, exp time.Duration) error {
	data, err := json.Marshal(logEntry)
	if err != nil {
		clients.SendFailureNotification("Redis SetProcessLog marshalling", err.Error(), time.Now().String(), "High")
		return fmt.Errorf("failed to marshal log entry: %w", err)
	}

	err = RedisClient.Set(ctx, key, data, exp).Err()
	if err != nil {
		clients.SendFailureNotification("Redis error", err.Error(), time.Now().String(), "High")
		return fmt.Errorf("failed to set log entry in Redis: %w", err)
	}

	return nil
}

func GetValidSubmissionKeys(ctx context.Context, epochID *big.Int, headers []string) ([]string, error) {
	allKeys := []string{}
	for _, header := range headers {
		keys := RedisClient.SMembers(ctx, SubmissionSetByHeaderKey(epochID.Uint64(), header)).Val()
		if len(keys) > 0 {
			allKeys = append(allKeys, keys...)
		}
	}
	return allKeys, nil
}

func ResetCollectorDBSubmissions(ctx context.Context, epochID *big.Int, headers []string) {
	// Define the set key based on whether epochID is provided
	var setKey string
	for _, header := range headers {
		setKey = SubmissionSetByHeaderKey(epochID.Uint64(), header)

		// Retrieve all keys from the set
		keysToDelete, err := RedisClient.SMembers(ctx, setKey).Result()
		if err != nil {
			log.Errorf("Error retrieving keys from set: %v", err)
			clients.SendFailureNotification("Redis error", err.Error(), time.Now().String(), "High")
			return
		}

		// Delete the keys
		if len(keysToDelete) > 0 {
			_, err := RedisClient.Del(ctx, keysToDelete...).Result()
			if err != nil {
				clients.SendFailureNotification("Redis error", err.Error(), time.Now().String(), "High")
				log.Errorf("Error deleting keys: %v", err)
			} else {
				log.Debugf("Deleted %d keys.\n", len(keysToDelete))
			}

			// Optionally, delete the set itself to clean up
			RedisClient.Del(ctx, setKey)
		} else {
			//log.Debugln("No keys found to delete.")
		}
	}
}
