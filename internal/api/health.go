package api

import (
	"context"
	"net/http"
	"time"

	"github.com/Amaan0907/Github-PR-Analyzer/internal/store"
	"github.com/gin-gonic/gin"
)

func Healthz(c *gin.Context) {
	c.JSON(http.StatusOK,gin.H{
		"status":"ok",
	})
}


func Readyz(s *store.Store) gin.HandlerFunc{
	return func(c *gin.Context) {
		ctx,cancel:=context.WithTimeout(c.Request.Context(),2*time.Second)
		defer cancel()


		status:=gin.H{}
		DBready:=true
		RedisReady:=true

		if err:=s.DB.Ping(ctx);err!=nil{
			status["database"]="down"
			DBready=false
		}else{
			status["database"]="up"
		}

		if err:=s.Redis.Ping(ctx).Err();err!=nil{
			status["redis"]="down"
			RedisReady=false
		}else{
			status["redis"]="up"
		}

		if!DBready || !RedisReady{
			c.JSON(http.StatusServiceUnavailable,status)
			return 
		}
		c.JSON(http.StatusOK,status)
	}
}