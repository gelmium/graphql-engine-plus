// FILEPATH: /workspaces/graphql-engine-plus/src/proxy/container_start.go
package main

import (
	"context"
	"math/rand"
	"net"
	"os"
	"regexp"
	"strconv"
	"time"

	gfshutdown "github.com/gelmium/graceful-shutdown"
	"github.com/gofiber/contrib/otelfiber"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/log"
	"github.com/gofiber/fiber/v2/middleware/logger"
	jsoniter "github.com/json-iterator/go"
	"github.com/valyala/fasthttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"
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

var tracer oteltrace.Tracer
var propagators propagation.TextMapPropagator
var otelExporter = os.Getenv("ENGINE_PLUS_ENABLE_OPEN_TELEMETRY")

func setRequestHeaderUpstream(c *fiber.Ctx, agent *fiber.Agent) {
	for k, v := range c.GetReqHeaders() {
		// filter out the header that we don't want to forward
		// such as: Accept-Encoding, Content-Length, Content-Type, Host
		if notForwardHeaderRegex.MatchString(k) {
			continue
		}
		agent.Set(k, v)
	}
	// add client IP Address to the end of the X-Forwarded-For header
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
		// check if error is timeout
		if errs[0].Error() == "timeout" {
			return c.Status(504).SendString("Upstream GraphQL Engine Timeout")
		}
		// log unhandled error and return 500
		log.Error(errs)
		return c.Status(500).SendString("Internal Server Error")
	}
	// return the response from the upstream url
	c.Set("Content-Type", "application/json")
	return c.Status(code).Send(body)
}

var primaryEngineServerProxyClient = &fasthttp.HostClient{
	Addr: "localhost:8881",
}
var replicaEngineServerProxyClient = &fasthttp.HostClient{
	Addr: "localhost:8882",
}

var scriptingServerProxyClient = &fasthttp.HostClient{
	Addr: "/tmp/scripting.sock",
	Dial: func(addr string) (net.Conn, error) {
		// Dial a Unix socket at path addr
		return net.DialUnix("unix", nil, &net.UnixAddr{
			Name: addr,
			Net:  "unix",
		})
	},
	// set other options here if required - most notably timeouts.
}

func prepareProxyRequest(req *fasthttp.Request, upstreamHost string, upstreamPath string, clientIpAddress string) {
	// do not proxy "Connection" header.
	req.Header.Del("Connection")
	// strip other unneeded headers.
	req.SetHost(upstreamHost)
	if upstreamPath != "" {
		req.SetRequestURI("http://" + upstreamHost + upstreamPath)
	}
	// alter other request params before sending them to upstream host
	if clientIpAddress != "" {
		// add client ip address to the end of X-Forwarded-For header
		req.Header.Add("X-Forwarded-For", clientIpAddress)
	}
}

func processProxyResponse(c *fiber.Ctx, resp *fasthttp.Response, redisCacheClient *RedisCacheClient, ttl int, familyCacheKey uint64, cacheKey uint64) {
	// do not proxy "Connection" header
	resp.Header.Del("Connection")
	// strip other unneeded headers
	if ttl != 0 && cacheKey > 0 && resp.StatusCode() == 200 && redisCacheClient != nil {
		ReadResponseBodyAndSaveToCache(c.Context(), resp, redisCacheClient, ttl, familyCacheKey, cacheKey, TraceOptions{tracer, c.UserContext()})
	}
}

var JsoniterConfigFastest = jsoniter.Config{
	EscapeHTML:                    false,
	MarshalFloatWith6Digits:       true,
	ObjectFieldMustBeSimpleString: true,
	TagKey:                        "json",
}.Froze()

// setup a fiber app which contain a simple /health endpoint which return a 200 status code
func setupFiber(startupCtx context.Context, startupReadonlyCtx context.Context, redisCacheClient *RedisCacheClient) *fiber.App {
	app := fiber.New(
		fiber.Config{
			ReadTimeout:  60 * time.Second,
			WriteTimeout: 60 * time.Second,
			IdleTimeout:  60 * time.Second,
			JSONEncoder:  JsoniterConfigFastest.Marshal,
			JSONDecoder:  JsoniterConfigFastest.Unmarshal,
			ServerHeader: "graphql-engine-plus/v1.0.0",
			Prefork:      false, // Prefork mode can't be enabled with sub process program running, also it doesn't give any performance boost if concurent request is low
		},
	)

	// get the PATH from environment variable
	var v1Path = os.Getenv("ENGINE_PLUS_GRAPHQL_V1_PATH")
	// default to /public/graphql/v1 if the env is not set
	if v1Path == "" {
		v1Path = "/public/graphql/v1"
	}
	// get the PATH from environment variable
	var roPath = os.Getenv("ENGINE_PLUS_GRAPHQL_V1_READONLY_PATH")
	// default to /public/graphql/v1readonly if the env is not set
	if roPath == "" {
		roPath = "/public/graphql/v1readonly"
	}
	// get the PATH from environment variable
	var execPath = os.Getenv("ENGINE_PLUS_PUBLIC_EXECUTE_PATH")

	// set logger middleware
	if os.Getenv("DEBUG") == "true" {
		log.SetLevel(log.LevelDebug)
	} else {
		log.SetLevel(log.LevelInfo)
	}
	app.Use(logger.New(logger.Config{
		Format:     "${time} \"${method} ${path}\" ${status} ${latency} (${bytesSent}) [${reqHeader:X-Request-ID};${reqHeader:Traceparent}] \"${reqHeader:Referer}\" \"${reqHeader:User-Agent}\"\n",
		TimeFormat: "2006-01-02T15:04:05.000000",
	}))

	// Compress middleware, we dont need to use this
	// as GraphQL Engine already compress the response
	// however, it only support gzip compression
	// enable this will allow us to support deflate and brotli compression as well
	// app.Use(compress.New(compress.Config{
	// 	Level: compress.LevelBestSpeed, // 1
	// }))

	// setup opentelemetry middleware for these endpoints
	if otelExporter == "grpc" || otelExporter == "http" {
		app.Use(otelfiber.Middleware(otelfiber.WithNext(func(c *fiber.Ctx) bool {
			// these endpoints will be traced
			if c.Path() == v1Path || c.Path() == roPath || (execPath != "" && c.Path() == execPath) {
				return false
			}
			// skip for others endpoint
			return true
		})))
	}

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
			// fire GET request to scripting-server to do healthcheck of
			// only primary-engine, as replica-engine is not yet ready
			agent = fiber.Get("http://localhost:8880/health/engine?not=replica")
		} else {
			// fire GET request to scripting-server to do full healthcheck of all engines
			agent = fiber.Get("http://localhost:8880/health/engine")
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
		// check if this is a query or mutation (can't be mutation)
		queryType := "query"
		if graphqlReq.IsMutationGraphQLRequest() {
			queryType = "mutation"
		}
		var redisKey string
		var ttl int
		var familyCacheKey, cacheKey uint64
		if queryType == "query" {
			// check if this is a cached query
			if ttl = graphqlReq.IsCachedQueryGraphQLRequest(); ttl != 0 && redisCacheClient != nil {
				familyCacheKey, cacheKey = CalculateCacheKey(c, graphqlReq)
				// check if the response body of this query has already cached in redis
				// if yes, return the response from redis
				redisKey = CreateRedisKey(familyCacheKey, cacheKey)
				if cacheData, err := redisCacheClient.Get(c.Context(), redisKey,
					TraceOptions{tracer, c.UserContext()}); err == nil {
					log.Debug("Cache hit for cacheKey: ", cacheKey)
					return SendCachedResponseBody(c, cacheData, ttl, familyCacheKey, cacheKey, TraceOptions{tracer, c.UserContext()})
				}
				log.Debug("Cache miss for cacheKey: ", cacheKey)
			}
		}
		req := c.Request()
		resp := c.Response()
		// prepare the proxy request
		prepareProxyRequest(req, "localhost:8881", "/v1/graphql", c.IP())
		// start tracer span
		spanCtx, span := tracer.Start(c.UserContext(), "primary-engine",
			oteltrace.WithSpanKind(oteltrace.SpanKindInternal),
			oteltrace.WithAttributes(
				attribute.String("graphql.operation.name", graphqlReq.OperationName),
				attribute.String("graphql.operation.type", queryType),
				attribute.String("graphql.document", graphqlReq.Query),
				attribute.String("http.request.header.x_request_id", c.Get("X-Request-ID")),
			))
		carrier := FastHttpHeaderCarrier{&req.Header}
		propagators.Inject(spanCtx, carrier)
		if err = primaryEngineServerProxyClient.DoTimeout(req, resp, UPSTREAM_TIME_OUT); err != nil {
			log.Error("Failed to request primary-engine:", err)
		}
		span.End() // end tracer span
		// process the proxy response
		processProxyResponse(c, resp, redisCacheClient, ttl, familyCacheKey, cacheKey)
		return err
	})

	// get the PATH from environment variable
	var engineMetaPath = os.Getenv("ENGINE_PLUS_META_PATH")
	// default to /public/meta/ if the env is not set
	if engineMetaPath == "" {
		engineMetaPath = "/public/meta/"
	}
	// check if metadataPath end with /
	// if not, add / to the end of metadataPath.
	if engineMetaPath[len(engineMetaPath)-1:] != "/" {
		engineMetaPath = engineMetaPath + "/"
	}
	// add a POST endpoint to forward request to an upstream url
	app.Post(engineMetaPath+"+", func(c *fiber.Ctx) error {
		// check and wait for startupCtx to be done
		waitForStartupToBeCompleted(startupCtx)
		// fire a POST request to the upstream url using the same header and body from the original request
		// any path after /public/meta/ will be appended to the upstream url
		// allow any type of request to be sent to the hasura graphql engine
		// for ex: /public/meta/v1/metadata -> /v1/metadata
		// for ex: /public/meta/v2/query -> /v2/query
		// read more here: https://hasura.io/docs/latest/api-reference/overview/
		agent := fiber.Post("http://localhost:8881/" + c.Params("+"))
		// send request to upstream without caching
		return sendRequestToUpstream(c, agent)
	})
	// add a GET endpoint to forward request to an upstream url
	app.Get(engineMetaPath+"+", func(c *fiber.Ctx) error {
		// check and wait for startupCtx to be done
		waitForStartupToBeCompleted(startupCtx)
		// for ex: GET /public/meta/v1/version -> /v1/version
		// fire a GET request to the upstream url using the same header and body from the original request
		agent := fiber.Get("http://localhost:8881/" + c.Params("+"))
		// send request to upstream without caching
		return sendRequestToUpstream(c, agent)
	})

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
		var redisKey string
		var ttl int
		var familyCacheKey, cacheKey uint64
		if ttl = graphqlReq.IsCachedQueryGraphQLRequest(); ttl != 0 && redisCacheClient != nil {
			familyCacheKey, cacheKey = CalculateCacheKey(c, graphqlReq)
			// check if the response body of this query has already cached in redis
			// if yes, return the response from redis
			redisKey = CreateRedisKey(familyCacheKey, cacheKey)
			if cacheData, err := redisCacheClient.Get(c.Context(), redisKey,
				TraceOptions{tracer, c.UserContext()}); err == nil {
				log.Debug("Cache hit for cacheKey: ", cacheKey)
				return SendCachedResponseBody(c, cacheData, ttl, familyCacheKey, cacheKey, TraceOptions{tracer, c.UserContext()})
			}
			log.Debug("Cache miss for cacheKey: ", cacheKey)
		}
		req := c.Request()
		resp := c.Response()
		// random an int from 0 to 99
		// if randomInt is less than primaryWeight, send request to primary upstream
		// if replica is not available, always send request to primary upstream
		if startupReadonlyCtx.Err() == nil || primaryVsReplicaWeight >= 100 || (primaryVsReplicaWeight > 0 && rand.Intn(100) < primaryVsReplicaWeight) {
			// prepare the proxy request to primary upstream
			prepareProxyRequest(req, "localhost:8881", "/v1/graphql", c.IP())
			// start tracer span
			spanCtx, span := tracer.Start(c.UserContext(), "primary-engine",
				oteltrace.WithSpanKind(oteltrace.SpanKindInternal),
				oteltrace.WithAttributes(
					attribute.String("graphql.operation.name", graphqlReq.OperationName),
					attribute.String("graphql.operation.type", "query"),
					attribute.String("graphql.document", graphqlReq.Query),
					attribute.String("http.request.header.x_request_id", c.Get("X-Request-ID")),
				))
			carrier := FastHttpHeaderCarrier{&req.Header}
			propagators.Inject(spanCtx, carrier)
			if err = primaryEngineServerProxyClient.DoTimeout(req, resp, UPSTREAM_TIME_OUT); err != nil {
				log.Error("Failed to request primary-engine:", err)
			}
			span.End() // end tracer span
		} else {
			// prepare the proxy request to replica upstream
			prepareProxyRequest(req, "localhost:8882", "/v1/graphql", c.IP())
			// start tracer span
			spanCtx, span := tracer.Start(c.UserContext(), "replica-engine",
				oteltrace.WithSpanKind(oteltrace.SpanKindInternal),
				oteltrace.WithAttributes(
					attribute.String("graphql.operation.name", graphqlReq.OperationName),
					attribute.String("graphql.operation.type", "query"),
					attribute.String("graphql.document", graphqlReq.Query),
					attribute.String("http.request.header.x_request_id", c.Get("X-Request-ID")),
				))
			carrier := FastHttpHeaderCarrier{&req.Header}
			propagators.Inject(spanCtx, carrier)
			if err = replicaEngineServerProxyClient.DoTimeout(req, resp, UPSTREAM_TIME_OUT); err != nil {
				log.Error("Failed to request replica-engine:", err)
			}
			span.End() // end tracer span
		}
		// process the proxy response
		processProxyResponse(c, resp, redisCacheClient, ttl, familyCacheKey, cacheKey)
		return err
	})

	// this endpoint is optional and will only be available if the env is set
	if execPath != "" {
		// this endpoint expose the scripting-server execute endpoint to public
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
			req := c.Request()
			resp := c.Response()
			// prepare the proxy request
			prepareProxyRequest(req, "localhost:8880", "/execute", c.IP())
			// start tracer span
			spanCtx, span := tracer.Start(c.UserContext(), "proxy -> scripting-server: /execute",
				oteltrace.WithSpanKind(oteltrace.SpanKindInternal),
				oteltrace.WithAttributes(
					attribute.String("http.request.header.x_request_id", c.Get("X-Request-ID")),
				))
			carrier := FastHttpHeaderCarrier{&req.Header}
			propagators.Inject(spanCtx, carrier)
			err := scriptingServerProxyClient.Do(req, resp)
			span.End() // end tracer span
			if err != nil {
				log.Error("Failed to request scripting-server:", err)
			}
			// process the proxy response
			processProxyResponse(c, resp, nil, 0, 0, 0)
			// check if X-Request-ID is set in the response header
			// if not, set it to the request id from the original request
			if resp.Header.Peek("X-Request-ID") == nil {
				resp.Header.Set("X-Request-ID", c.Get("X-Request-ID"))
			}
			return err
		})
	}
	return app
}

var redisUrlRemoveUnexpectedOptionRegex = regexp.MustCompile(`(?i)(\?)?&?(ssl_cert_reqs=\w+)`)

func setupRedisClient(ctx context.Context) *RedisCacheClient {
	// setup redis client
	redisUrl := os.Getenv("HASURA_GRAPHQL_REDIS_CLUSTER_URL")
	if redisUrl == "" {
		redisUrl = os.Getenv("HASURA_GRAPHQL_REDIS_URL")
	}
	groupcacheOptions := NewGroupCacheOptions()
	// parse groupcacheOptions.waitETA from env
	if waitETA, err := strconv.ParseInt(os.Getenv("ENGINE_PLUS_GROUPCACHE_WAIT_ETA"), 10, 64); err == nil && waitETA >= 0 {
		groupcacheOptions.waitETA = uint64(waitETA)
	}
	redisCacheClient, err := NewRedisCacheClient(
		ctx,
		redisUrl,
		os.Getenv("HASURA_GRAPHQL_REDIS_READER_URL"),
		"http://127.0.0.1:8879",
		groupcacheOptions,
	)
	if err != nil {
		log.Error("Failed to setup Redis Client: ", err)
		return nil
	}
	// ping redis server
	if err := redisCacheClient.redisClient.Ping(ctx).Err(); err != nil {
		log.Error("Failed to connect to Redis: ", err)
	}
	if err := redisCacheClient.redisClientReader.Ping(ctx).Err(); err != nil {
		log.Error("Failed to connect to Redis: ", err)
	}
	log.Info("Connected to Redis Cache")
	return redisCacheClient
}

func main() {
	mainCtx, mainCtxCancelFn := context.WithCancel(context.Background())
	startupCtx, startupDoneFn := context.WithTimeout(mainCtx, 60*time.Second)
	startupReadonlyCtx, startupReadonlyDoneFn := context.WithTimeout(mainCtx, 180*time.Second)
	// this must happen as early as possible
	otelTracerProvider := InitTracerProvider(mainCtx, otelExporter)
	// setup resources
	redisCacheClient := setupRedisClient(mainCtx)
	// these 2 line need to happen after InitTracerProvider
	tracer = otel.Tracer("graphql-engine-plus")
	propagators = otel.GetTextMapPropagator()
	// setup app
	app := setupFiber(startupCtx, startupReadonlyCtx, redisCacheClient)
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
	// start the engine servers
	serverShutdownErrorChanel := make(chan error)
	startServerCtx, startServerCtxCancelFn := context.WithCancel(mainCtx)
	go StartGraphqlEngineServers(startServerCtx, mainCtxCancelFn, startupCtx, startupDoneFn, startupReadonlyCtx, startupReadonlyDoneFn, serverShutdownErrorChanel)

	// register engine-servers & http-server clean-up operations
	// then wait for termination signal to do the clean-up
	shutdownTimeout := 30 * time.Second
	cleanUpOps := map[string]gfshutdown.Operation{
		"engine-servers": func(shutdownCtx context.Context) error {
			startServerCtxCancelFn()
			return <-serverShutdownErrorChanel
		},
		"http-server": func(shutdownCtx context.Context) error {
			return app.ShutdownWithTimeout(shutdownTimeout - 1*time.Second)
		},
		// Add other cleanup operations here
	}
	if redisCacheClient != nil {
		cleanUpOps["cache-clients"] = func(shutdownCtx context.Context) error {
			if redisCacheClient != nil {
				// we dont need to close groupcache server as it will be closed
				// together with the fiber http server
				return redisCacheClient.Close(shutdownCtx, false)
			} else {
				return nil
			}
		}
	}
	if otelExporter == "grpc" || otelExporter == "http" {
		// add opentelemetry tracer clean-up operation
		cleanUpOps["opentelemetry-tracer"] = func(shutdownCtx context.Context) error {
			return otelTracerProvider.Shutdown(shutdownCtx)
		}
	}
	wait := gfshutdown.GracefulShutdown(mainCtx, shutdownTimeout, cleanUpOps)
	exitCode := <-wait
	log.Info("GraphQL Engine Plus is shutdown")
	os.Exit(exitCode)
}
