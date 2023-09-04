package main

import (
	"context"
	"os"
	"strconv"
	"time"

	gfshutdown "github.com/gelmium/graceful-shutdown"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/log"
	"github.com/gofiber/fiber/v2/middleware/logger"
)

// setup a fiber app which contain a simple /health endpoint which return a 200 status code
func setupFiber() *fiber.App {
	app := fiber.New(
		fiber.Config{
			ReadTimeout:  60 * time.Second,
			WriteTimeout: 60 * time.Second,
			ServerHeader: "App Runner Demo",
			Prefork:      false,
		},
	)
	app.Use(logger.New(logger.Config{
		Format:       "${time} \"${method} ${path}\"${status} ${latency} (${bytesSent}) \"${reqHeader:Referer}\" \"${reqHeader:User-Agent}\"\n",
		TimeFormat:   "2006-01-02T15:04:05.000000",
		TimeInterval: 10 * time.Millisecond,
	}))
	app.Get("/health", func(c *fiber.Ctx) error {
		// read GET parameter from sleep from url localhost:3000/health?sleep=30
		sleep := c.Query("sleep")
		// ParseInt string sleep to int
		sleepDuration, err := strconv.ParseInt(sleep, 10, 64)
		// sleep for the given time, sleepDuration is in microseconds
		if err == nil && sleepDuration > 0 {
			// sleep max 110 seconds, as App Runner will always timeout after 120 seconds
			time.Sleep(time.Duration(min(sleepDuration, 110_000_000)) * time.Microsecond)
		}
		return c.SendStatus(fiber.StatusOK)
	})

	return app
}

func main() {
	app := setupFiber()
	// set log level to debug if DEBUG=true
	debugFlag, err := strconv.ParseBool(os.Getenv("DEBUG"))
	if err == nil && debugFlag {
		log.DefaultLogger().SetLevel(log.LevelDebug)
	} else {
		log.DefaultLogger().SetLevel(log.LevelInfo)
	}

	workerCtx, cancelWorkerFn := context.WithCancel(context.Background())
	// define a channel to wait for graceful shutdown
	workerGracefulShutdownChanel := make(chan error)
	go SetupRedisWorker(workerCtx, workerGracefulShutdownChanel)
	go app.Listen(":3000")

	// wait for termination signal and register database & http server clean-up operations
	shutdownTimeout := 30 * time.Second
	wait := gfshutdown.GracefulShutdown(context.Background(), shutdownTimeout, map[string]gfshutdown.Operation{
		"redis-worker": func(ctx context.Context) error {
			cancelWorkerFn()
			return <-workerGracefulShutdownChanel
		},
		"http-server": func(ctx context.Context) error {
			return app.ShutdownWithTimeout(shutdownTimeout - 1*time.Second)
		},
		// Add other cleanup operations here
	})
	os.Exit(<-wait)
}
