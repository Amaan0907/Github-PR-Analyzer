package store

import (
	"context"

	"github.com/redis/go-redis/v9"
)

func NewRedis(ctx context.Context,redisURL string)(*redis.Client,error){
	opts,err:=redis.ParseURL(redisURL)

	if err!=nil{
		return nil,err
	}

	client:=redis.NewClient(opts)

	if err:=client.Ping(ctx).Err();err!=nil{
		client.Close()
		return nil, err
	}

	return client,err
}

