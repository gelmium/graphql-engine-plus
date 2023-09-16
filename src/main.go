// FILEPATH: /workspaces/graphql-engine-plus/src/proxy/container_start.go
package main

import (
	"context"
	"math/rand"
	"os"
	"regexp"
	"strconv"
	"time"

	gfshutdown "github.com/gelmium/graceful-shutdown"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/log"
	"github.com/gofiber/fiber/v2/middleware/compress"
	"github.com/gofiber/fiber/v2/middleware/logger"
)

// App Runner will always timeout after 120 seconds
const UPSTREAM_TIME_OUT = 110 * time.Second

func waitForStartupToBeCompleted(startupCtx context.Context) {
	if startupCtx.Err() == nil {
		log.Warn("Waiting for startup to be completed")
		// this wait can last for max 60s
		<-startupCtx.Done()
	}
}

var notForwardHeaderRegex = regexp.MustCompile(`(?i)^(Host|Accept-Encoding|Content-Length|Content-Type)$`)

func setRequestHeaderUpstream(c *fiber.Ctx, agent *fiber.Agent) {
	for k, v := range c.GetReqHeaders() {
		// filter out the header that we don't want to forward
		// such as: Accept-Encoding, Content-Length, Content-Type, Host
		if notForwardHeaderRegex.MatchString(k) {
			continue
		}
		agent.Set(k, v)
	}
	// set the X-Forwarded-For header to the client IP
	agent.Add("X-Forwarded-For", c.IP())
}

func sendRequestToUpstream(c *fiber.Ctx, agent *fiber.Agent) error {
	// loop through the header and set the header from the original request
	setRequestHeaderUpstream(c, agent)
	agent.Body(c.Body())
	// set the timeout of proxy request
	agent.Timeout(UPSTREAM_TIME_OUT)
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
			return c.Status(504).SendString("Upstream GraphQL Engine Timeout")
		}
		return c.Status(500).SendString("Internal Server Error")
	}
	// return the response from the upstream url
	c.Set("Content-Type", "application/json")
	return c.Status(code).Send(body)
}

// setup a fiber app which contain a simple /health endpoint which return a 200 status code
func setupFiber(startupCtx context.Context, startupReadonlyCtx context.Context) *fiber.App {
	app := fiber.New(
		fiber.Config{
			ReadTimeout:  60 * time.Second,
			WriteTimeout: 60 * time.Second,
			ServerHeader: "graphql-engine-plus/v1.0.0",
			Prefork:      false, // Prefork mode can't be enabled with sub process program running, also it doesn't give any performance boost if concurent request is low
		},
	)
	app.Use(logger.New(logger.Config{
		Format:     "${time} \"${method} ${path}\"${status} ${latency} (${bytesSent}) [${reqHeader:X-Request-ID}] \"${reqHeader:Referer}\" \"${reqHeader:User-Agent}\"\n",
		TimeFormat: "2006-01-02T15:04:05.000000",
	}))

	// Compress middleware
	app.Use(compress.New(compress.Config{
		Level: compress.LevelBestSpeed, // 1
	}))

	// get the HEALTH_CHECK_PATH from environment variable
	var healthCheckPath = os.Getenv("ENGINE_PLUS_HEALTH_CHECK_PATH")
	// default to /public/graphql/health if the env is not set
	if healthCheckPath == "" {
		healthCheckPath = "/public/graphql/health"
	}
	app.Get(healthCheckPath, func(c *fiber.Ctx) error {
		// check for startupCtx to be done
		if startupCtx.Err() == nil {
			log.Warn("Startup has not yet completed, return OK for health check anyway")
			return c.SendStatus(fiber.StatusOK)
			// Or we can wait for startupCtx to be done here: <-startupCtx.Done()
		}
		var agent *fiber.Agent
		if startupReadonlyCtx.Err() == nil {
			// fire GET request to scripting server to do healthcheck of
			// only primary engine, as replica engine is not yet ready
			agent = fiber.Get("http://localhost:8888/health/engine?not=replica")
		} else {
			// fire GET request to scripting server to do full healthcheck of all engines
			agent = fiber.Get("http://localhost:8888/health/engine")
		}
		if err := agent.Parse(); err != nil {
			log.Error(err)
		}
		code, body, errs := agent.Bytes()
		if len(errs) > 0 {
			log.Error(errs)
			return c.Status(500).SendString(errs[0].Error())
		}
		// return the response from the upstream url
		return c.Status(code).Send(body)
	})
	// standard health check endpoint
	app.Get("/health", func(c *fiber.Ctx) error {
		// read GET parameter from sleep from url eg: localhost:3000/health?sleep=30
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
	// get the PATH from environment variable
	var v1Path = os.Getenv("ENGINE_PLUS_GRAPHQL_V1_PATH")
	// default to /public/graphql/v1 if the env is not set
	if v1Path == "" {
		v1Path = "/public/graphql/v1"
	}
	primaryVsReplicaWeight := 100
	if os.Getenv("HASURA_GRAPHQL_READ_REPLICA_URLS") != "" {
		primaryVsReplicaWeight = 50
		// parse the primary weight from env, convert it to int
		primaryWeightInt, err := strconv.Atoi(os.Getenv("ENGINE_PLUS_GRAPHQL_PRIMARY_VS_REPLICA_WEIGHT"))
		if err == nil {
			primaryVsReplicaWeight = primaryWeightInt
		}
	}

	// add a POST endpoint to forward request to an upstream url
	app.Post(v1Path, func(c *fiber.Ctx) error {
		// check and wait for startupCtx to be done
		waitForStartupToBeCompleted(startupCtx)
		// check if this is read only request
		graphqlReq, err := ParseGraphQLRequest(c)
		if err != nil {
			// return a Fiber error if can't parse the request
			return err
		}

		if graphqlReq.IsSubscriptionGraphQLRequest() {
			// TODO: support subscription via another websocket endpoint in the future
			return fiber.NewError(fiber.StatusForbidden, "GraphQL Engine Plus does not support subscription yet")
		}

		// fire a POST request to the upstream url using the same header and body from the original request
		agent := fiber.Post("http://localhost:8881/v1/graphql")
		// send mutation request to primary upstream
		return sendRequestToUpstream(c, agent)
	})

	// get the PATH from environment variable
	var metadataPath = os.Getenv("ENGINE_PLUS_METADATA_PATH")
	// default to /public/metadata/v1 if the env is not set
	if metadataPath == "" {
		metadataPath = "/public/metadata/"
	}
	// check if metadataPath end with /
	// if not, add / to the end of metadataPath.
	if metadataPath[len(metadataPath)-1:] != "/" {
		metadataPath = metadataPath + "/"
	}
	// add a POST endpoint to forward request to an upstream url
	app.Post(metadataPath+"+", func(c *fiber.Ctx) error {
		// check and wait for startupCtx to be done
		waitForStartupToBeCompleted(startupCtx)
		// fire a POST request to the upstream url using the same header and body from the original request
		agent := fiber.Post("http://localhost:8881/" + c.Params("+"))
		// send request to upstream without caching
		return sendRequestToUpstream(c, agent)
	})
	// add a GET endpoint to forward request to an upstream url
	app.Get(metadataPath+"+", func(c *fiber.Ctx) error {
		// check and wait for startupCtx to be done
		waitForStartupToBeCompleted(startupCtx)
		// fire a POST request to the upstream url using the same header and body from the original request
		agent := fiber.Get("http://localhost:8881/" + c.Params("+"))
		// send request to upstream without caching
		return sendRequestToUpstream(c, agent)
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
		waitForStartupToBeCompleted(startupCtx)
		// check if this is read only request
		graphqlReq, err := ParseGraphQLRequest(c)
		if err != nil {
			// return a Fiber error if can't parse the request
			return err
		}
		if graphqlReq.IsMutationGraphQLRequest() {
			// return a Fiber error if the request is a mutation
			return fiber.NewError(fiber.StatusForbidden, "readonly endpoint does not allow mutation")
		}
		if graphqlReq.IsSubscriptionGraphQLRequest() {
			// TODO: support subscription via another websocket endpoint in the future
			return fiber.NewError(fiber.StatusForbidden, "GraphQL Engine Plus does not support subscription yet")
		}

		// random an int from 0 to 99
		// if randomInt is less than primaryWeight, send request to primary upstream
		// if replica is not available, always send request to primary upstream
		if startupReadonlyCtx.Err() == nil || primaryVsReplicaWeight >= 100 || (primaryVsReplicaWeight > 0 && rand.Intn(100) < primaryVsReplicaWeight) {
			agent := fiber.Post("http://localhost:8881/v1/graphql")
			// send query request to primary upstream
			return sendRequestToUpstream(c, agent)
		} else {
			// fire a POST request to the upstream url using the same header and body from the original request
			agent := fiber.Post("http://localhost:8880/v1/graphql")
			// send query request to replica upstream
			return sendRequestToUpstream(c, agent)
		}
	})

	// get the PATH from environment variable
	var execPath = os.Getenv("ENGINE_PLUS_PUBLIC_EXECUTE_PATH")
	// this endpoint is optional and will only be available if the env is set
	if execPath != "" {
		// this endpoint expose the scripting server execute endpoint to public
		// this allow client to by pass GraphQL engine and execute script directly
		// be careful when exposing this endpoint to public without a strong security measure
		app.Post(execPath, func(c *fiber.Ctx) error {
			// validate the engine plus execute secret
			headerHasuraAdminSecret := c.Get("X-Engine-Plus-Execute-Secret")
			envHasuraAdminSecret := os.Getenv("ENGINE_PLUS_EXECUTE_SECRET")
			// if header is empty or not equal to the env, return 401
			if headerHasuraAdminSecret == "" || headerHasuraAdminSecret != envHasuraAdminSecret {
				return fiber.NewError(fiber.StatusUnauthorized, "Unauthorized")
			}
			// check and wait for startupCtx to be done
			waitForStartupToBeCompleted(startupCtx)
			// fire a POST request to the upstream url using the same header and body from the original request
			agent := fiber.Post("http://localhost:8888/execute")
			// send request to upstream without caching
			return sendRequestToUpstream(c, agent)
		})
	}
	return app
}

func main() {
	mainCtx, mainCtxCancelFn := context.WithCancel(context.Background())
	startupCtx, startupDoneFn := context.WithTimeout(mainCtx, 60*time.Second)
	startupReadonlyCtx, startupReadonlyDoneFn := context.WithTimeout(mainCtx, 60*time.Second)
	app := setupFiber(startupCtx, startupReadonlyCtx)
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
	go StartGraphqlEngineServers(startServerCtx, mainCtxCancelFn, startupCtx, startupDoneFn, startupReadonlyCtx, startupReadonlyDoneFn, serverShutdownErrorChanel)

	// register engine-servers & http-server clean-up operations
	// then wait for termination signal to do the clean-up
	shutdownTimeout := 30 * time.Second
	wait := gfshutdown.GracefulShutdown(mainCtx, shutdownTimeout, map[string]gfshutdown.Operation{
		"engine-servers": func(shutdownCtx context.Context) error {
			startServerCtxCancelFn()
			return <-serverShutdownErrorChanel
		},
		"http-server": func(shutdownCtx context.Context) error {
			return app.ShutdownWithTimeout(shutdownTimeout - 1*time.Second)
		},
		// Add other cleanup operations here
	})
	log.Info("GraphQL Engine Plus is shutdown")
	os.Exit(<-wait)
}
