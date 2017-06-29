package service

import (
	"fmt"
	"time"

	"github.com/bobziuchkovski/cue"
	"github.com/gin-gonic/gin"
)

func ginLogger(name string) gin.HandlerFunc {
	log := cue.NewLogger(name)
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		log.Debugf("%s %s -> %d in %v %s",
			c.Request.Method,
			c.Request.URL.Path,
			c.Writer.Status(),
			time.Since(start),
			c.Errors.String(),
		)
	}
}

func ginRecovery(name string) gin.HandlerFunc {
	log := cue.NewLogger(name)
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				switch err.(type) {
				case error:
					_ = c.Error(err.(error))
				default:
					_ = c.Error(fmt.Errorf("unknown error: %v", err))
				}
			}

			if len(c.Errors) == 0 {
				return
			}

			for _, err := range c.Errors {
				// #nosec
				_ = log.WithValue(
					"request", c.Request,
				).Error(err, "gin handler failed")
			}

			c.JSON(500, gin.H{
				"errors": c.Errors,
			})
		}()

		c.Next()
	}
}
