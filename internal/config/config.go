package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	RedisAddr          string
	HTTPPort           string
	MetricsPort        string
	WorkerConcurrency  int
	HeartbeatInterval  time.Duration
	JobTimeout         time.Duration
	ReaperInterval     time.Duration
	StalenessThreshold time.Duration
}

func Load() *Config {
	return &Config{
		RedisAddr:          getEnv("REDIS_ADDR", "localhost:6379"),
		HTTPPort:           getEnv("HTTP_PORT", "8080"),
		MetricsPort:        getEnv("METRICS_PORT", "2112"),
		WorkerConcurrency:  getEnvInt("WORKER_CONCURRENCY", 5),
		HeartbeatInterval:  getEnvDuration("HEARTBEAT_INTERVAL", 5*time.Second),
		JobTimeout:         getEnvDuration("JOB_TIMEOUT", 30*time.Second),
		ReaperInterval:     getEnvDuration("REAPER_INTERVAL", 10*time.Second),
		StalenessThreshold: getEnvDuration("STALENESS_THRESHOLD", 60*time.Second),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
