package config

import (
	"fmt"
	"os"
)

type Config struct {
	Port        string;
	DatabaseUrl string;
	RedisUrl    string;
	Environment string;
}

func Load() Config {
	return Config{
		Port: requiredEnv("PORT"),
		DatabaseUrl: requiredEnv("DATABASE_URL"),
		RedisUrl: requiredEnv("REDIS_URL"),
		Environment: requiredEnv("ENVIRONMENT"),
	}
}

func requiredEnv(key string) string {
	value := os.Getenv(key)

	if value==""{
		panic(fmt.Sprintf("required environment variable %s is missing",key))
	}
	return value


}