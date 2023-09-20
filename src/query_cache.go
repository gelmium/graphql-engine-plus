package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/log"
	"github.com/redis/go-redis/v9"
	"github.com/valyala/fasthttp"
)

type CacheResponseRedis struct {
	// Body            []byte `json:"body"  redis:"body"`
	ExpiresAt       int64  `json:"expires_at" redis:"expires_at"`
	ContentType     string `json:"content_type" redis:"content_type"`
	ContentEncoding string `json:"content_encoding" redis:"content_encoding"`
}

func SendCachedResponseBody(c *fiber.Ctx, cacheData []byte, ttl int, familyCacheKey uint64, cacheKey uint64) error {
	// first 8 bytes of the cacheData is the length of the response body
	// read the length of the response body
	bodyLength := binary.LittleEndian.Uint64(cacheData[:8])
	// read the response cacheBody
	cacheBody := cacheData[8 : 8+bodyLength]
	// read the response cacheMeta
	redisCachedResponseResult := CacheResponseRedis{}
	if err := JsoniterConfigFastest.Unmarshal(cacheData[8+bodyLength:], &redisCachedResponseResult); err != nil {
		log.Error("Error when unmarshal cache meta: ", err)
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
	return c.Status(200).Send(cacheBody)
}

func ReadResponseBodyAndSaveToCache(ctx context.Context, resp *fasthttp.Response, redisClient redis.UniversalClient, ttl int, familyCacheKey uint64, cacheKey uint64) {
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
		log.Error("Error when marshal cache: ", err)
		return
	}
	cacheData = append(cacheData, cacheMeta...)
	// cacheData consist of the response body and the cache meta
	redisKey := CreateRedisKey(familyCacheKey, cacheKey)
	if err := redisClient.Set(ctx, redisKey, cacheData, time.Duration(ttl)*time.Second).Err(); err != nil {
		log.Error("Error when save cache to redis: ", err)
		return
	}
	resp.Header.Set("X-Hasura-Query-Cache-Key", strconv.FormatUint(cacheKey, 10))
	resp.Header.Set("X-Hasura-Query-Family-Cache-Key", strconv.FormatUint(familyCacheKey, 10))
	resp.Header.Set("Cache-Control", "max-age="+strconv.Itoa(ttl))
}

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
	for k, v := range c.GetReqHeaders() {
		// filter out the header that we don't want to cache
		// such as: Host, Connection, ...
		if notCacheHeaderRegex.MatchString(k) {
			continue
		}
		if k == "Authorization" {
			// TODO: read the JWT token from the request header Authorization
			// add the user and role to the hash
			continue
		}
		// print the header key and value
		header := k + ":" + v

		headers = append(headers, header)
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
