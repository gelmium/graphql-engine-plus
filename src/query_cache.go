package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"os"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/log"
	"github.com/valyala/fasthttp"
	oteltrace "go.opentelemetry.io/otel/trace"
)

type CacheResponseRedis struct {
	// Body            []byte `json:"body"  redis:"body"`
	ExpiresAt       int64  `json:"expires_at" redis:"expires_at"`
	ContentType     string `json:"content_type" redis:"content_type"`
	ContentEncoding string `json:"content_encoding" redis:"content_encoding"`
}

func SendCachedResponseBody(c *fiber.Ctx, cacheData []byte, ttl int, familyCacheKey uint64, cacheKey uint64, traceOpts TraceOptions) error {
	// start tracer span
	_, span := traceOpts.tracer.Start(traceOpts.ctx, "SendCachedResponseBody",
		oteltrace.WithSpanKind(oteltrace.SpanKindInternal),
	)
	defer span.End() // end tracer span
	// first 8 bytes of the cacheData is the length of the response body
	// read the length of the response body
	bodyLength := binary.LittleEndian.Uint64(cacheData[:8])
	// read the response cacheBody
	cacheBody := cacheData[8 : 8+bodyLength]
	// read the response cacheMeta
	redisCachedResponseResult := CacheResponseRedis{}
	if err := JsoniterConfigFastest.Unmarshal(cacheData[8+bodyLength:], &redisCachedResponseResult); err != nil {
		log.Error("Failed to unmarshal cache meta: ", err)
		span.RecordError(err)
		return err
	}
	// set the response header
	c.Set("Content-Type", redisCachedResponseResult.ContentType)
	// check if Accept-Encoding in request header contain gzip
	// if yes, set the Content-Encoding to gzip
	if redisCachedResponseResult.ContentEncoding != "" {
		c.Set("Content-Encoding", redisCachedResponseResult.ContentEncoding)
	}
	c.Set("X-Hasura-Query-Cache-Key", strconv.FormatUint(cacheKey, 10))
	c.Set("X-Hasura-Query-Family-Cache-Key", strconv.FormatUint(familyCacheKey, 10))
	// caculate max-age based on the cache expiresAt
	maxAge := redisCachedResponseResult.ExpiresAt - time.Now().Unix()
	if maxAge < 0 {
		maxAge = 0
	}
	c.Set("Cache-Control", "max-age="+strconv.FormatInt(maxAge, 10))
	// get the span context and trace ids
	spanContext := span.SpanContext()
	traceId := spanContext.TraceID().String()
	spanId := spanContext.SpanID().String()
	traceFlags := spanContext.TraceFlags().String()
	// construct the Traceparent header using above values
	c.Request().Header.Set("Traceparent", "00-"+traceId+"-"+spanId+"-"+traceFlags)
	return c.Status(200).Send(cacheBody)
}

func ReadResponseBodyAndSaveToCache(ctx context.Context, resp *fasthttp.Response, redisCacheClient *RedisCacheClient, ttl int, familyCacheKey uint64, cacheKey uint64, traceOpts TraceOptions) {
	// start tracer span
	spanCtx, span := traceOpts.tracer.Start(traceOpts.ctx, "ReadResponseBodyAndSaveToCache",
		oteltrace.WithSpanKind(oteltrace.SpanKindInternal),
	)
	defer span.End() // end tracer span
	// read the response body and store it in redis
	body := resp.Body()
	// store the body length in the first 8 bytes
	cacheData := make([]byte, 8)
	binary.LittleEndian.PutUint64(cacheData, uint64(len(body)))
	// then append the body
	cacheData = append(cacheData, body...)
	// then append the cache meta in json format
	cacheMeta, err := JsoniterConfigFastest.Marshal(&CacheResponseRedis{
		time.Now().Add(time.Duration(ttl) * time.Second).Unix(),
		string(resp.Header.Peek("Content-Type")),
		string(resp.Header.Peek("Content-Encoding")),
	})
	if err != nil {
		log.Error("Failed to marshal cache: ", err)
		return
	}
	cacheData = append(cacheData, cacheMeta...)
	// cacheData consist of the response body and the cache meta
	redisKey := CreateRedisKey(familyCacheKey, cacheKey)
	if err := redisCacheClient.Set(ctx, redisKey, cacheData, time.Duration(ttl)*time.Second,
		TraceOptions{tracer, spanCtx}); err != nil {
		log.Error("Failed to save cache to redis: ", err)
		span.RecordError(err)
		return
	}
	resp.Header.Set("X-Hasura-Query-Cache-Key", strconv.FormatUint(cacheKey, 10))
	resp.Header.Set("X-Hasura-Query-Family-Cache-Key", strconv.FormatUint(familyCacheKey, 10))
	resp.Header.Set("Cache-Control", "max-age="+strconv.Itoa(ttl))
}

var jwtAuthParserConfig = ReadHasuraGraphqlJwtSecretConfig(os.Getenv("HASURA_GRAPHQL_JWT_SECRET"))

// TODO: allow this Regex to be configurable via environment variable
var notCacheHeaderRegex = regexp.MustCompile(`(?i)^(Host|Connection|X-Forwarded-For|X-Request-ID|User-Agent|Content-Length|Content-Type|X-Envoy-External-Address|X-Envoy-Expected-Rq-Timeout-Ms)$`)

func CalculateCacheKey(c *fiber.Ctx, graphqlReq *GraphQLRequest) (uint64, uint64) {
	// calculate the hash of the request using
	// the GraphQL query
	// the GraphQL operation name
	// the GraphQL variables of the query
	// request headers
	h := fnv.New64a()
	h.Write([]byte(graphqlReq.Query))
	h.Write([]byte(graphqlReq.OperationName))
	// loop through the header from the original request
	// and add it to the hash
	headers := []string{}
	for k, vList := range c.GetReqHeaders() {
		// loop through the header values vList
		for _, v := range vList {
			// filter out the header that we don't want to cache
			// such as: Host, Connection, ...
			if notCacheHeaderRegex.MatchString(k) {
				continue
			}
			if (k == jwtAuthParserConfig.Header.Type || k == "Authorization") && v[:7] == "Bearer " {
				// TODO: read the JWT token from the request header
				// extract token from the header by remove "Bearer a"
				tokenString := string(v[7:])
				claims, err := jwtAuthParserConfig.ParseJwt(tokenString)
				// remove timestamp from claims
				delete(claims, "exp")
				delete(claims, "nbf")
				delete(claims, "iat")
				// remove jwt id from claims
				delete(claims, "jti")
				// TODO: traverse the claims and remove the claims that we don't want to cache using Regular Expression
				// the RegEx should be configurable via environment variable
				if err != nil {
					log.Error("Failed to parse JWT token: ", err)
				} else {
					// convert the entire claims to json string
					// and add it to the hash
					// this claims has the timestamp removed already.
					if claimsJsonByteString, err := JsoniterConfigFastest.Marshal(claims); err != nil {
						log.Error("Failed to marshal claims: ", err)
					} else {
						log.Debug("Compute cache keys with claims: ", claims)
						h.Write(claimsJsonByteString)
						// continue to skip this header
						continue
					}
				}
				// other while this header key and value will be added to the hash as below
			}
			// print the header key and value
			header := k + ":" + v
			headers = append(headers, header)
		}
	}
	// sort the header key and value
	// to make sure that the hash is always the same
	// even if the header key and value is not in the same order
	sort.Strings(headers)
	log.Debug("Compute cache keys with headers: ", headers)
	for _, header := range headers {
		h.Write([]byte(header))
	}
	familyCacheKey := h.Sum64()
	h.Write([]byte(fmt.Sprint(graphqlReq.Variables)))
	cacheKey := h.Sum64()
	return familyCacheKey, cacheKey
}

func CreateRedisKey(familyKey uint64, cacheKey uint64) string {
	return "graphql-engine-plus:" + strconv.FormatUint(familyKey, 36) + ":" + strconv.FormatUint(cacheKey, 36)
}
