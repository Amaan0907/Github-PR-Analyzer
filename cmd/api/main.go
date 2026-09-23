package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Amaan0907/Github-PR-Analyzer/internal/api"
	"github.com/Amaan0907/Github-PR-Analyzer/internal/config"
	"github.com/Amaan0907/Github-PR-Analyzer/internal/store"
)

func main() {
	logger:=slog.New(slog.NewJSONHandler(os.Stdout,nil))
	slog.SetDefault(logger)
	ctx,stop:=signal.NotifyContext(context.Background(),syscall.SIGINT,syscall.SIGTERM)

	defer stop()

	cfg:=config.Load()

	db,err:=store.New(ctx,cfg.DatabaseUrl,cfg.RedisUrl)

	if err!=nil{
		logger.Error("failed to connect to store","error",err)
		os.Exit(1)
	}
	logger.Info("Connection to Database established")
	defer db.Close()


	router:=api.NewRouter(db,logger)
	

	srv:=&http.Server{
		Addr: ":"+cfg.Port,
		Handler: router,
	}

	go func(){
		logger.Info("listening","addr",srv.Addr)
		if err:=srv.ListenAndServe();err!=nil &&!errors.Is(err,http.ErrServerClosed){
			logger.Error("server error","error",err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()

	logger.Info("shutdown signal received")


	shutdownCtx,cancel:=context.WithTimeout(context.Background(),10*time.Second)

	defer  cancel()

	if err :=srv.Shutdown(shutdownCtx);err!=nil{
		logger.Error("Forced shutdown","error",err)
	}
	logger.Info("Server exited cleanly")
}