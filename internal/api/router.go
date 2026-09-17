package api

import (
	"github.com/Amaan0907/Github-PR-Analyzer/internal/store"
	"github.com/gin-gonic/gin"
)

func NewRouter(s *store.Store)*gin.Engine{
	r:=gin.New()

	r.Use(gin.Recovery())
	r.GET("/healthz",Healthz)
	r.GET("/readyz",Readyz(s))

	return r
}