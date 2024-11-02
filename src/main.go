package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"time"

	gfshutdown "github.com/gelmium/graceful-shutdown"
	"github.com/gofiber/contrib/otelfiber"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/log"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/proxy"
	jsoniter "github.com/json-iterator/go"
	"github.com/spf13/viper"
	"github.com/valyala/fasthttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// Set a hard timeout after 110 seconds
const UPSTREAM_TIME_OUT = 110 * time.Second

// Set a hard timeout for read
const FIBER_READ_TIME_OUT = 30 * time.Second

// Set a hard timeout for write
const FIBER_WRITE_TIME_OUT = 30 * time.Second

// keep-alive is enabled by default, connection is keep alive after serving a request
// if there is no incoming request after 120 seconds it will be closed
const FIBER_IDLE_TIME_OUT = 120 * time.Second

// The total time allowed for all process to gracefully shutdown
// Should set to a number equal or greater than READ_TIME_OUT
const GRACEFUL_SHUTDOWN_TIMEOUT = 30 * time.Second

func waitForStartupToBeCompleted(startupCtx context.Context) {
	if startupCtx.Err() == nil {
		log.Warn("Waiting for startup to be completed")
		// this wait can last for max 60s
		<-startupCtx.Done()
	}
}

var tracer oteltrace.Tracer
var propagators propagation.TextMapPropagator

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
		// start tracer span by wrapping the context with timeout same as fiber Context timeout
		traceCtx, _ := WrapContextCancelByAnotherContext(c.UserContext(), c.Context(), 0)
		ReadResponseBodyAndSaveToCache(traceCtx, resp, redisCacheClient, ttl, familyCacheKey, cacheKey)
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
			ReadTimeout:  FIBER_READ_TIME_OUT,
			WriteTimeout: FIBER_WRITE_TIME_OUT,
			IdleTimeout:  FIBER_IDLE_TIME_OUT,
			JSONEncoder:  JsoniterConfigFastest.Marshal,
			JSONDecoder:  JsoniterConfigFastest.Unmarshal,
			ServerHeader: "graphql-engine-plus/v1.0.0",
			Prefork:      false, // Prefork mode can't be enabled with sub process program running, also it doesn't give any performance boost if concurent request is low
		},
	)
	var rwPath = viper.GetString(ENGINE_PLUS_GRAPHQL_V1_PATH)
	var roPath = viper.GetString(ENGINE_PLUS_GRAPHQL_V1_READONLY_PATH)
	var scriptingPublicPath = StripTrailingSlash(viper.GetString(ENGINE_PLUS_SCRIPTING_PUBLIC_PATH))

	// set logger middleware
	if debugMode {
		log.SetLevel(log.LevelDebug)
		handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
		slog.SetDefault(slog.New(handler))
	} else {
		log.SetLevel(log.LevelInfo)
		handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
		slog.SetDefault(slog.New(handler))
	}
	app.Use(logger.New(logger.Config{
		Format:     "${time} \"${method} ${path}\" ${status} ${latency} (${bytesSent}) [${reqHeader:X-Request-ID};${reqHeader:Traceparent}] \"${reqHeader:Referer}\" \"${reqHeader:User-Agent}\"\n",
		TimeFormat: "2006-01-02T15:04:05.000000",
		// skip health check endpoints
		Next: func(c *fiber.Ctx) bool {
			// Next defines a function to skip this middleware when returned true.
			if c.Path() == viper.GetString(ENGINE_PLUS_HEALTH_CHECK_PATH) || c.Path() == "/health" {
				return true
			}
			return false
		},
	}))

	// CORS middleware
	app.Use(cors.New(cors.Config{
		AllowOrigins: hasuraGqlCorsDomain,
	}))

	// Compress middleware, we dont need to use this
	// as GraphQL Engine already compress the response
	// however, it only support gzip compression
	// enable this will allow us to support deflate and brotli compression as well
	// app.Use(compress.New(compress.Config{
	// 	Level: compress.LevelBestSpeed, // 1
	// }))

	// setup opentelemetry middleware for these endpoints
	if engineEnableOtelType == "grpc" || engineEnableOtelType == "http" {
		app.Use(otelfiber.Middleware(otelfiber.WithNext(func(c *fiber.Ctx) bool {
			// these endpoints will be traced
			if c.Path() == rwPath || c.Path() == roPath || (scriptingPublicPath != "" && c.Path() == scriptingPublicPath) {
				return false
			}
			// skip for others endpoint
			return true
		})))
	}

	app.Get(viper.GetString(ENGINE_PLUS_HEALTH_CHECK_PATH), func(c *fiber.Ctx) error {
		// check for startupCtx to be done
		if startupCtx.Err() == nil {
			log.Warn("Startup has not yet completed, return health check not OK")
			return c.SendStatus(fiber.StatusServiceUnavailable)
		}
		// startup has completed, we can now do the health check for dependent services
		var url string
		if startupReadonlyCtx.Err() == nil {
			// fire GET request to scripting-server to do healthcheck of
			// only primary-engine, as replica-engine is not yet ready
			url = "http://localhost:8880/health/engine?quite=true&not=replica"
		} else {
			// fire GET request to scripting-server to do full healthcheck of all engines
			url = "http://localhost:8880/health/engine?quite=true"
		}
		if err := proxy.Do(c, url); err != nil {
			return err
		}
		// Remove Server header from response
		c.Response().Header.Del(fiber.HeaderServer)
		return nil
	})
	// standard health check endpoint
	app.Get("/health", func(c *fiber.Ctx) error {
		// check if startupCtx is done
		if startupCtx.Err() == nil {
			log.Warn("Startup has not yet completed, return health check not OK")
			return c.SendStatus(fiber.StatusServiceUnavailable)
		}
		return c.SendStatus(fiber.StatusOK)
	})

	mutationHandler := graphqlMutationHandlerFactory()
	queryHandler := graphqlQueryHandlerFactory(startupReadonlyCtx, redisCacheClient)
	// add a POST endpoint to forward request to an upstream url
	app.Post(rwPath, func(c *fiber.Ctx) error {
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
		// check if this is a query or mutation
		if graphqlReq.IsMutationGraphQLRequest() {
			// pass to read write handler since this is a graphql muration
			return mutationHandler(c, graphqlReq)
		} else {
			// pass to read only handler if this is a graphql query
			return queryHandler(c, graphqlReq)
		}
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
		return queryHandler(c, graphqlReq)
	})

	// Proxy request to an upstream url
	app.All(engineMetaPath+"+", func(c *fiber.Ctx) error {
		// check and wait for startupCtx to be done
		waitForStartupToBeCompleted(startupCtx)
		// Ex if engineMetaPath = "/public/meta"
		// for ex: POST /public/meta/v1/metadata -> /v1/metadata
		// for ex: POST /public/meta/v2/query -> /v2/query
		// for ex: GET /public/meta/v1/version -> /v1/version
		// proxy GET request to the upstream using the same header and body from the original request
		url := "http://localhost:8881" + c.Params("+")
		if err := proxy.Do(c, url); err != nil {
			return err
		}
		// Remove Server header from response
		c.Response().Header.Del(fiber.HeaderServer)
		return nil
	})

	// this endpoint is optional and will only be available if the env is set
	if scriptingPublicPath != "" {
		// this endpoint expose the scripting-server execute endpoint to public
		// this allow client to by pass GraphQL engine and execute script directly
		// be careful when exposing this endpoint to public without a strong security measure
		app.All(scriptingPublicPath+"+", func(c *fiber.Ctx) error {
			// validate the engine plus execute secret
			headerExecuteSecret := c.Get("X-Engine-Plus-Execute-Secret")
			// if header is empty or not equal to the env, return 401
			if headerExecuteSecret == "" || headerExecuteSecret != viper.GetString(ENGINE_PLUS_EXECUTE_SECRET) {
				return fiber.NewError(fiber.StatusUnauthorized, "Unauthorized")
			}
			// do not allow request with header X-Engine-Plus-Execute-Url
			// when ENGINE_PLUS_ALLOW_EXECURL is false (return 403)
			if !viper.GetBool(ENGINE_PLUS_ALLOW_EXECURL) && c.Get("X-Engine-Plus-Execute-Url") != "" {
				return fiber.NewError(fiber.StatusForbidden, "Forbidden")
			}
			// check and wait for startupCtx to be done
			waitForStartupToBeCompleted(startupCtx)
			// Ex if engineMetaPath = "/scripting"
			// for ex: POST /scripting/upload -> /upload
			// for ex: POST /scripting/execute -> /execute
			// for ex: GET /scripting/validate -> /validate
			// for ex: GET /scripting/health/engine -> /health/engine
			req := c.Request()
			resp := c.Response()
			// prepare the proxy request
			prepareProxyRequest(req, "localhost:8880", c.Params("+"), c.IP())
			// start tracer span
			spanCtx, span := tracer.Start(c.UserContext(), "proxy -> scripting-server",
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

func setupRedisClient(ctx context.Context) *RedisCacheClient {
	// setup redis client

	if hasuraGqlRedisUrl == "" && hasuraGqlRedisClusterUrl == "" {
		return nil
	}
	groupcacheOptions := NewGroupCacheOptions()
	// set groupcacheOptions.waitETA using value from env
	groupcacheOptions.waitETA = engineGroupcacheWaitEta
	groupcacheOptions.cacheMaxSize = int64(engineGroupcacheMaxSize)

	// get list of available addresses
	localAddress := "127.0.0.1"
	if engineGroupcacheClusterMode == "aws_ec2" {
		// get the local ipv4 address of the ec2 instance via the meta-data url
		address, err := GetIpFromAwsEc2Metadata()
		if err != nil {
			log.Error(err)
		} else {
			localAddress = address
		}
	} else if engineGroupcacheClusterMode == "aws_ecs" {
		// get the local ipv4 address of the container instance via the ecs meta-data
		address, err := GetIpFromAwsEcsContainerMetadata()
		if err != nil {
			log.Error(err)
		} else {
			localAddress = address
		}
	} else if engineGroupcacheClusterMode == "host_ipnet" {
		address, err := GetIpFromHostNetInterfaces()
		if err != nil {
			log.Error(err)
		} else {
			localAddress = address
		}
	} else {
		groupcacheOptions.disableAutoDiscovery = true
	}

	if groupcacheServerPort == "" {
		groupcacheServerPort = "8879"
	}
	redisCacheClient, err := NewRedisCacheClient(
		ctx,
		hasuraGqlRedisClusterUrl,
		hasuraGqlRedisUrl,
		hasuraGqlRedisReaderUrl,
		"http://"+localAddress+":"+groupcacheServerPort,
		groupcacheOptions,
	)
	if err != nil {
		log.Error("Failed to setup Redis Client: ", err)
		return nil
	}
	// ping redis server
	if err := redisCacheClient.redisClient.Ping(ctx).Err(); err != nil {
		log.Error("Failed to connect to Redis Server: ", err)
		return nil
	}
	if err := redisCacheClient.redisClientReader.Ping(ctx).Err(); err != nil {
		log.Error("Failed to connect to Redis Reader Server: ", err)
		return nil
	}
	log.Info("Connected to Redis Cache")
	return redisCacheClient
}

func main() {
	// Setup viper
	SetupViper()
	// Init config with default values
	InitConfig()
	// These context are for startup only
	mainCtx, mainCtxCancelFn := context.WithCancel(context.Background())
	startupCtx, startupDoneFn := context.WithTimeout(mainCtx, 60*time.Second)
	startupReadonlyCtx, startupReadonlyDoneFn := context.WithTimeout(mainCtx, 180*time.Second)
	// this must happen as early as possible
	otelTracerProvider := InitTracerProvider(mainCtx, engineEnableOtelType)
	// setup resources
	redisCacheClient := setupRedisClient(mainCtx)
	// these 2 line need to happen after InitTracerProvider
	tracer = otel.Tracer("graphql-engine-plus")
	propagators = otel.GetTextMapPropagator()
	// setup app
	app := setupFiber(startupCtx, startupReadonlyCtx, redisCacheClient)

	// start the http server in coroutine and handler error
	// if the server failed to start
	// we dont need to wait for the server to start
	// as we will wait for the startup to be completed
	go func() {
		err := app.Listen(viper.GetString("engineServerHost") + ":" + viper.GetString("engineServerPort"))
		if err != nil {
			// trigger graceful shutdown
			mainCtxCancelFn()
		}
	}()
	// start the engine servers
	serverShutdownErrorChanel := make(chan error)
	startServerCtx, startServerCtxCancelFn := context.WithCancel(mainCtx)
	go StartGraphqlEngineServers(startServerCtx, mainCtxCancelFn, startupCtx, startupDoneFn, startupReadonlyCtx, startupReadonlyDoneFn, serverShutdownErrorChanel)

	// register engine-servers & http-server clean-up operations
	// then wait for termination signal to do the clean-up
	cleanUpOps := map[string]gfshutdown.Operation{
		"engine-servers": func(shutdownCtx context.Context) error {
			startServerCtxCancelFn()
			return <-serverShutdownErrorChanel
		},
		"http-server": func(shutdownCtx context.Context) error {
			return app.ShutdownWithTimeout(GRACEFUL_SHUTDOWN_TIMEOUT - 1*time.Second)
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
	if engineEnableOtelType == "grpc" || engineEnableOtelType == "http" {
		// add opentelemetry tracer clean-up operation
		cleanUpOps["opentelemetry-tracer"] = func(shutdownCtx context.Context) error {
			return otelTracerProvider.Shutdown(shutdownCtx)
		}
	}
	wait := gfshutdown.GracefulShutdown(mainCtx, GRACEFUL_SHUTDOWN_TIMEOUT, cleanUpOps)
	exitCode := <-wait
	log.Info("GraphQL Engine Plus is shutdown")
	os.Exit(exitCode)
}
