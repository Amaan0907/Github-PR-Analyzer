package store

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

type Store struct {
	DB *pgxpool.Pool
	Redis *redis.Client
}


func New(ctx context.Context,databaseURL string,redisURL string)(*Store,error){
	pool,err:=NewPostgres(ctx,databaseURL)

	if err!=nil{
		pool.Close()
		return nil,err
	}

	client,err:=NewRedis(ctx,redisURL)

	if err!=nil{
		client.Close()
		return nil,err
	}

	return &Store{
		DB: pool,
		Redis: client,
	},nil
}

func(s *Store) Close(){
	s.DB.Close()
	s.Redis.Close()
}

