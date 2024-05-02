package main

import (
	"context"
	"math/rand"
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
			ReadTimeout:  20 * time.Second,
			WriteTimeout: 20 * time.Second,
			ServerHeader: "App Runner Proxy Benchmark",
			Prefork:      true,
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

	// add a POST endpoint to forward request to an upstream url
	app.Post("/proxy", func(c *fiber.Ctx) error {
		url := c.Query("url")
		ctx, cancel := context.WithTimeout(c.Context(), 28*time.Second)
		defer cancel()
		for tries := 0; tries < 99; tries++ {
			// fire a POST request to the upstream url using the same header and body from the original request
			agent := fiber.Post(url)
			agent.Timeout(28 * time.Second)
			// loop through the header and set the header from the original request
			for k, v := range c.GetReqHeaders() {
				agent.Set(k, v[0])
			}
			agent.Body(c.Body())

			// send the request to the upstream url using Fiber Go
			if err := agent.Parse(); err != nil {
				log.Error(err)
				return c.Status(500).SendString("Internal Server Error")
			}
			code, body, errs := agent.Bytes()
			if len(errs) > 0 {
				log.Error(errs)
				// check if error is timeout
				if errs[0].Error() == "timeout" {
					return c.Status(504).SendString("Upstream Timeout")
				}
				return c.Status(500).SendString("Internal Server Error")
			}
			if code == 429 {
				// if the upstream return 429, sleep for a while + jitter and try again
				t := time.Duration(tries*10)*time.Millisecond + time.Duration(rand.Intn(300+30*tries))*time.Millisecond
				log.Infof("Upstream response 429, Retry after %s", t)
				time.Sleep(t)
				// check for ctx cancellation after sleep
				select {
				case <-ctx.Done():
					log.Error("Context cancelled")
					return c.Status(503).SendString("Service Unavailable (context cancelled)")
				default:
					continue
				}
			}
			// return the response from the upstream url
			c.Set("Content-Type", "application/json")
			return c.Status(code).Send(body)
		}
		log.Error("Max retry reached")
		return c.Status(503).SendString("Service Unavailable (max retry reached)")
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
	// start the proxy http server
	go app.Listen(":3000")

	// wait for termination signal and register database & http server clean-up operations
	shutdownTimeout := 30 * time.Second
	wait := gfshutdown.GracefulShutdown(context.Background(), shutdownTimeout, map[string]gfshutdown.Operation{
		"http-server": func(ctx context.Context) error {
			return app.ShutdownWithTimeout(shutdownTimeout - 1*time.Second)
		},
		// Add other cleanup operations here
	})
	os.Exit(<-wait)
}
