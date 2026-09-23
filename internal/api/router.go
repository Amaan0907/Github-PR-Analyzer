package api

import (
	"log/slog"

	"github.com/Amaan0907/Github-PR-Analyzer/internal/store"
	
	"github.com/gin-gonic/gin"
)

func NewRouter(s *store.Store,logger *slog.Logger)*gin.Engine{
	r:=gin.New()
	r.Use(RequestId())
	r.Use(Logger(logger))
	r.Use(Recovery(logger))

	
	r.GET("/healthz",Healthz)
	r.GET("/readyz",Readyz(s))

	return r
}