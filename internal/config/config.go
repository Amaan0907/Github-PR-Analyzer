package config

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	Port        string;
	DatabaseUrl string;
	RedisUrl    string;
	Environment string;
}

func Load() Config {
	_=godotenv.Load()
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