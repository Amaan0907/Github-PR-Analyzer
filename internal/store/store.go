package store

import (
	"context"
	"fmt"

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
		return nil,fmt.Errorf("Unable to create connection pool: %w",err)
	}
	
	client,err:=NewRedis(ctx,redisURL)
	
	if err!=nil{
		pool.Close()
		return nil,fmt.Errorf("Unable to create connection client: %w",err)
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

