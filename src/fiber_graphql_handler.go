package main

import (
	"context"
	"math/rand"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/log"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
)

func graphqlMutationHandlerFactory() func(c *fiber.Ctx, graphqlReq *GraphQLRequest) error {
	return func(c *fiber.Ctx, graphqlReq *GraphQLRequest) error {
		req := c.Request()
		resp := c.Response()
		// prepare the proxy request
		prepareProxyRequest(req, "localhost:8881", "/v1/graphql", c.IP())
		// start tracer span
		spanCtx, span := tracer.Start(c.UserContext(), "primary-engine",
			oteltrace.WithSpanKind(oteltrace.SpanKindInternal),
			oteltrace.WithAttributes(
				attribute.String("graphql.operation.name", graphqlReq.OperationName),
				attribute.String("graphql.operation.type", "mutation"),
				attribute.String("graphql.document", graphqlReq.Query),
				attribute.String("http.request.header.x_request_id", c.Get("X-Request-ID")),
			))
		carrier := FastHttpHeaderCarrier{&req.Header}
		propagators.Inject(spanCtx, carrier)
		err := primaryEngineServerProxyClient.DoTimeout(req, resp, UPSTREAM_TIME_OUT)
		if err != nil {
			log.Error("Failed to request primary-engine:", err)
		}
		span.End() // end tracer span
		// process the proxy response
		processProxyResponse(c, resp, nil, 0, 0, 0)
		return err
	}
}

func graphqlQueryHandlerFactory(startupReadonlyCtx context.Context, redisCacheClient *RedisCacheClient) func(c *fiber.Ctx, graphqlReq *GraphQLRequest) error {
	return func(c *fiber.Ctx, graphqlReq *GraphQLRequest) error {
		// handle the fiber request after graphql validation
		var redisKey string
		var ttl int
		var familyCacheKey, cacheKey uint64
		if ttl = graphqlReq.IsCachedQueryGraphQLRequest(); ttl != 0 && redisCacheClient != nil {
			familyCacheKey, cacheKey = CalculateCacheKey(c, graphqlReq)
			// check if the response body of this query has already cached in redis
			// if yes, return the response from redis
			redisKey = CreateRedisKey(familyCacheKey, cacheKey)
			// set timeout to underlying context to avoid blocking for too long (3.3s)
			traceCtx, cancelFunc := WrapContextCancelByAnotherContext(c.UserContext(), c.Context(), 3300)
			defer cancelFunc()
			if cacheData, err := redisCacheClient.Get(traceCtx, redisKey); err == nil {
				// log.Debug("Cache hit for cacheKey: ", cacheKey)
				return SendCachedResponseBody(c, cacheData, ttl, familyCacheKey, cacheKey, TraceOptions{tracer, c.UserContext()})
			}
			log.Debug("Cache miss for cacheKey: ", cacheKey)
		}
		req := c.Request()
		resp := c.Response()
		err := error(nil)
		// random an int from 0 to 99
		// if randomInt is less than primaryWeight, send request to primary upstream
		// if replica is not available, always send request to primary upstream
		if hasuraGqlReadReplicaUrls == "" || startupReadonlyCtx.Err() == nil || engineGqlPvRweight >= 100 || (engineGqlPvRweight > 0 && rand.Intn(100) < int(engineGqlPvRweight)) {
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
	}
}
