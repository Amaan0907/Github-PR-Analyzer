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

	store,err:=store.New(ctx,cfg.DatabaseUrl)

	if err!=nil{
		log.Fatal(err)
	}
	fmt.Println("Connect to DataBase extablished")
	defer store.Close()



}