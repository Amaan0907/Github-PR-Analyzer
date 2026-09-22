package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/Amaan0907/Github-PR-Analyzer/internal/api"
	"github.com/Amaan0907/Github-PR-Analyzer/internal/config"
	"github.com/Amaan0907/Github-PR-Analyzer/internal/store"
	
)

func main() {
	ctx,stop:=signal.NotifyContext(context.Background(),syscall.SIGINT,syscall.SIGTERM)

	defer stop()

	cfg:=config.Load()

	db,err:=store.New(ctx,cfg.DatabaseUrl,cfg.RedisUrl)

	if err!=nil{
		log.Fatal(err)
	}
	fmt.Println("Connect to DataBase extablished")
	defer db.Close()


	router:=api.NewRouter(db)
	

	srv:=&http.Server{
		Addr: ":"+cfg.Port,
		Handler: router,
	}

	go func(){
		log.Printf("Listening on %s",srv.Addr)
		if err:=srv.ListenAndServe();err!=nil &&!errors.Is(err,http.ErrServerClosed){
			log.Fatal("server error: %v",err)
		}
	}()

	<-ctx.Done()

	log.Println("shutdown signal received")


	shutdownCtx,cancel:=context.WithTimeout(context.Background(),10*time.Second)

	defer  cancel()

	if err :=srv.Shutdown(shutdownCtx);err!=nil{
		log.Fatalf("Forced shutdown: %v",err)
	}
	log.Println("Server exited cleanly")
}