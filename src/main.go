// FILEPATH: /workspaces/graphql-engine-plus/src/proxy/container_start.go
package main

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/log"
	"github.com/gofiber/fiber/v2/middleware/logger"
)

// App Runner will always timeout after 120 seconds
const GLOBAL_TIME_OUT = 110 * time.Second

// setup a fiber app which contain a simple /health endpoint which return a 200 status code
func Setup(startupCtx context.Context) *fiber.App {
	app := fiber.New(
		fiber.Config{
			ReadTimeout:  60 * time.Second,
			WriteTimeout: 60 * time.Second,
			ServerHeader: "Graphql Engine Plus",
		},
	)
	app.Use(logger.New(logger.Config{
		Format:     "${time} \"${method} ${path}\"${status} ${latency} (${bytesSent}) \"${reqHeader:Referer}\" \"${reqHeader:User-Agent}\"\n",
		TimeFormat: "2006-01-02T15:04:05.000000",
	}))
	// get the HEALTH_CHECK_PATH from environment variable
	var healthCheckPath = os.Getenv("ENGINE_PLUS_HEALTH_CHECK_PATH")
	// default to /public/graphql/health if the env is not set
	if healthCheckPath == "" {
		healthCheckPath = "/public/graphql/health"
	}
	app.Get(healthCheckPath, func(c *fiber.Ctx) error {
		// fire GET request to scripting server to do full healthcheck of all engines
		agent := fiber.Get("http://localhost:8888/health/engine")
		if err := agent.Parse(); err != nil {
			log.Error(err)
		}
		code, body, errs := agent.Bytes()
		if len(errs) > 0 {
			log.Error(errs)
		}
		// return the response from the upstream url
		return c.Status(code).Send(body)
	})
	// standard health check endpoint
	app.Get("/health", func(c *fiber.Ctx) error {
		// read GET parameter from sleep from url
		sleep := c.Query("sleep")
		// ParseInt string sleep to int
		sleepDuration, err := strconv.ParseInt(sleep, 10, 64)
		// sleep for the given time, sleepDuration is in microseconds
		if err == nil && sleepDuration > 0 {
			// sleep max equal to GLOBAL_TIME_OUT
			time.Sleep(time.Duration(min(sleepDuration, GLOBAL_TIME_OUT.Microseconds())) * time.Microsecond)
		}
		return c.Status(fiber.StatusOK).SendString("OK")
	})
	// get the PATH from environment variable
	var v1Path = os.Getenv("ENGINE_PLUS_GRAPHQL_V1_PATH")
	// default to /public/graphql/v1 if the env is not set
	if v1Path == "" {
		v1Path = "/public/graphql/v1"
	}
	// add a POST endpoint to forward request to an upstream url
	app.Post(v1Path, func(c *fiber.Ctx) error {
		// check and wait for startupCtx to be done
		if startupCtx.Err() == nil {
			log.Warn("Waiting for startup to be completed")
			// this wait can last for max 60s
			<-startupCtx.Done()
		}
		// fire a POST request to the upstream url using the same header and body from the original request
		agent := fiber.Post("http://localhost:8881/v1/graphql")
		// loop through the header and set the header from the original request
		for k, v := range c.GetReqHeaders() {
			agent.Set(k, v)
		}
		agent.Body(c.Body())
		// set the timeout of proxy request
		agent.Timeout(GLOBAL_TIME_OUT)
		// send the request to the upstream url using Fiber Go
		if err := agent.Parse(); err != nil {
			log.Error(err)
			return c.Status(500).SendString("Internal Server Error")
		}
		code, body, errs := agent.Bytes()
		if len(errs) > 0 {
			log.Error(errs)
			return c.Status(500).SendString("Internal Server Error")
		}
		// return the response from the upstream url
		return c.Status(code).Send(body)
	})

	// get the PATH from environment variable
	var v2Path = os.Getenv("ENGINE_PLUS_GRAPHQL_V2_PATH")
	// default to /public/graphql/v2 if the env is not set
	if v2Path == "" {
		v2Path = "/public/graphql/v2"
	}
	// add a POST endpoint to forward request to an upstream url
	app.Post(v2Path, func(c *fiber.Ctx) error {
		// check and wait for startupCtx to be done
		if startupCtx.Err() == nil {
			log.Warn("Waiting for startup to be completed")
			// this wait can last for max 60s
			<-startupCtx.Done()
		}
		// fire a POST request to the upstream url using the same header and body from the original request
		agent := fiber.Post("http://localhost:8882/v1/graphql")
		// loop through the header and set the header from the original request
		for k, v := range c.GetReqHeaders() {
			agent.Set(k, v)
		}
		agent.Body(c.Body())
		// set the timeout of proxy request
		agent.Timeout(GLOBAL_TIME_OUT)
		// send the request to the upstream url using Fiber Go
		if err := agent.Parse(); err != nil {
			log.Error(err)
			return c.Status(500).SendString("Internal Server Error")
		}
		code, body, errs := agent.Bytes()
		if len(errs) > 0 {
			log.Error(errs)
			return c.Status(500).SendString("Internal Server Error")
		}
		// return the response from the upstream url
		return c.Status(code).Send(body)
	})

	// get the PATH from environment variable
	var roPath = os.Getenv("ENGINE_PLUS_GRAPHQL_V1_READONLY_PATH")
	// default to /public/graphql/v1readonly if the env is not set
	if roPath == "" {
		roPath = "/public/graphql/v1readonly"
	}
	// add a POST endpoint to forward request to an upstream url
	app.Post(roPath, func(c *fiber.Ctx) error {
		// check and wait for startupCtx to be done
		if startupCtx.Err() == nil {
			log.Warn("Waiting for startup to be completed")
			// this wait can last for max 60s
			<-startupCtx.Done()
		}
		// check if this is read only request
		graphqlReq, err := ParseGraphQLRequest(c)
		if err != nil {
			// return a Fiber error if can't parse the request
			return err
		}
		if IsMutationGraphQLRequest(graphqlReq) {
			// return a Fiber error if the request is a mutation
			return fiber.NewError(fiber.StatusForbidden, "readonly endpoint does not allow mutation")
		}
		if IsSubscriptionGraphQLRequest(graphqlReq) {
			// TODO: support subscription via another websocket endpoint in the future
			return fiber.NewError(fiber.StatusForbidden, "GraphQL Engine Plus does not support subscription yet")
		}

		// fire a POST request to the upstream url using the same header and body from the original request
		agent := fiber.Post("http://localhost:8880/v1/graphql")
		// loop through the header and set the header from the original request
		for k, v := range c.GetReqHeaders() {
			agent.Set(k, v)
		}
		agent.Body(c.Body())
		// set the timeout of proxy request
		agent.Timeout(GLOBAL_TIME_OUT)
		// send the request to the upstream url using Fiber Go
		if err := agent.Parse(); err != nil {
			log.Error(err)
			return c.Status(500).SendString("Internal Server Error")
		}
		code, body, errs := agent.Bytes()
		if len(errs) > 0 {
			log.Error(errs)
			return c.Status(500).SendString("Internal Server Error")
		}
		// return the response from the upstream url
		return c.Status(code).Send(body)
	})
	return app
}

func main() {
	mainCtx, mainCtxCancelFn := context.WithCancel(context.Background())
	startupCtx, startupDoneFn := context.WithTimeout(mainCtx, 60*time.Second)
	app := Setup(startupCtx)
	// get the server Host:Port from environment variable
	var serverHost = os.Getenv("ENGINE_PLUS_SERVER_HOST")
	// default to empty string if the env is not set
	var serverPort = os.Getenv("ENGINE_PLUS_SERVER_PORT")
	// default to 8000 if the env is not set
	if serverPort == "" {
		serverPort = "8000"
	}
	// start the http server
	go app.Listen(serverHost + ":" + serverPort)

	serverShutdownErrorChanel := make(chan error)
	startServerCtx, startServerCtxCancelFn := context.WithCancel(mainCtx)
	go StartGraphqlEngineServers(startServerCtx, mainCtxCancelFn, startupCtx, startupDoneFn, serverShutdownErrorChanel)

	// register engine-servers & http-server clean-up operations
	// then wait for termination signal to do the clean-up
	shutdownTimeout := 30 * time.Second
	wait := GracefulShutdown(mainCtx, shutdownTimeout, map[string]operation{
		"engine-servers": func(shutdownCtx context.Context) error {
			startServerCtxCancelFn()
			return <-serverShutdownErrorChanel
		},
		"http-server": func(shutdownCtx context.Context) error {
			return app.ShutdownWithTimeout(shutdownTimeout - 1*time.Second)
		},
		// Add other cleanup operations here
	})
	<-wait
	log.Info("GraphQL Engine Plus is shutdown")
}
