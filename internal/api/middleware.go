package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

type ctxKey string

const requestIDKey ctxKey = "reques_id"

func RequestId() gin.HandlerFunc{
	return func(c *gin.Context) {
		id:=c.GetHeader("X-Request-ID")

		if id==""{
			id=GenerateRequestID()
		}

		ctx:=context.WithValue(c,requestIDKey,id)
		c.Request=c.Request.WithContext(ctx)

		c.Writer.Header().Set("X-Request-ID",id)
		c.Next()

	}
}


func GenerateRequestID() string{
	b:=make([]byte,16)
	_,_=rand.Read(b)

	return hex.EncodeToString(b)
}

func RequestIDFromContext(ctx context.Context)string{
	id,_:=ctx.Value(requestIDKey).(string)
	return id
}


func Logger(logger *slog.Logger) gin.HandlerFunc{
	return func(c *gin.Context) {
		start:=time.Now()

		c.Next()

		logger.Info("request",
		"request_id",RequestIDFromContext(c.Request.Context()),
		"methods",c.Request.Method,
		"path",c.Request.URL.Path,
		"status",c.Writer.Status(),
		"latency_ms",time.Since(start).Milliseconds(),
		"client_ip",c.ClientIP(),
	)
	}
}

func Recovery(logger *slog.Logger) gin.HandlerFunc{
	return func(c *gin.Context) {
		defer func ()  {
			rec:=recover(); if rec!= nil{
				logger.Error("panic recovered",
					"request_id",RequestIDFromContext(c.Request.Context()),
					"error",rec,
					"path",c.Request.URL.Path,
			)
			c.AbortWithStatus(http.StatusInternalServerError)
			}
		}()
		c.Next()
	}
}