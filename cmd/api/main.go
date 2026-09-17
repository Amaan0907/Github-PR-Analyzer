package main

import (
	"context"
	"fmt"
	"log"

	"github.com/Amaan0907/Github-PR-Analyzer/internal/config"
	"github.com/Amaan0907/Github-PR-Analyzer/internal/store"
)

func main() {
	ctx := context.Background()

	cfg:=config.Load()

	db,err:=store.New(ctx,cfg.DatabaseUrl,cfg.RedisUrl)

	if err!=nil{
		log.Fatal(err)
	}
	fmt.Println("Connect to DataBase extablished")
	defer db.Close()

	

}