package main

import (
	"context"
	"fmt"
	"log"

	"github.com/Amaan0907/Github-PR-Analyzer/internal/api"
	"github.com/Amaan0907/Github-PR-Analyzer/internal/config"
	"github.com/Amaan0907/Github-PR-Analyzer/internal/store"
)

func main() {
	ctx := context.Background()

	cfg:=config.Load()
	fmt.Printf("DATABASE_URL = %q\n", cfg.DatabaseUrl)
	db,err:=store.New(ctx,cfg.DatabaseUrl,cfg.RedisUrl)

	if err!=nil{
		log.Fatal(err)
	}
	fmt.Println("Connect to DataBase extablished")
	defer db.Close()


	router:=api.NewRouter(db)
	router.Run(":"+cfg.Port)


	

}