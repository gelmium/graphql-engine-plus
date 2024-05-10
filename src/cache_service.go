package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mailgun/groupcache/v2"
	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
)

type RedisCacheClient struct {
	redisClient         redis.UniversalClient
	redisClientReader   redis.UniversalClient
	groupcacheClient    *groupcache.Group
	groupcacheServerUrl string
	groupcacheServer    http.Server
	// OTEL tracer
	tracer oteltrace.Tracer
}

const GroupCacheName = "rediscaches"
const GroupCacheClusterNodesUrlKey = GroupCacheName + ".groupcache.cluster"

type GroupcacheOptions struct {
	// default to 60MiB
	cacheMaxSize int64
	// by seting it > 0, the others process will wait up to ETA (miliseconds) for first process to populate the cache data
	// this will avoid the thundering herd problem. value default to 1000 ms.
	waitETA uint64
	// disable the groupcache auto discovery feature
	disableAutoDiscovery bool
	poolOptions          groupcache.HTTPPoolOptions
}

func NewGroupCacheOptions() (options GroupcacheOptions) {
	options.cacheMaxSize = 60000000 // in bytes
	options.waitETA = 1000          // in miliseconds
	return
}

var redisUrlRemoveUnexpectedOptionRegex = regexp.MustCompile(`(?i)(\?)?&?(ssl_cert_reqs=\w+)`)
var groupcacheNodeUrlRegex *regexp.Regexp

func NewRedisCacheClient(ctx context.Context, redisClusterUrl string, redisUrl string, redisReaderUrl string, groupcacheUrls string, groupcacheOptions GroupcacheOptions) (*RedisCacheClient, error) {
	client := &RedisCacheClient{}
	// check if redisUrl is started with rediss://
	if redisClusterUrl != "" {
		// remove unexpected option from clusterUrl using regex
		clusterUrl := redisUrlRemoveUnexpectedOptionRegex.ReplaceAllString(redisClusterUrl, "$1")
		if clusterOpt, err := redis.ParseClusterURL(clusterUrl); err == nil {
			// Disable TLS verification by default
			clusterOpt.TLSConfig.InsecureSkipVerify = true
			// duplicate clusterOpt for reader node
			readOnlyClusterOpt := clusterOpt
			// init the redis client for write cache
			client.redisClient = redis.NewClusterClient(clusterOpt)
			// add options for reader client
			readOnlyClusterOpt.RouteByLatency = true
			readOnlyClusterOpt.ReadOnly = true
			// init the redis client for read cache
			client.redisClientReader = redis.NewClusterClient(readOnlyClusterOpt)
		} else {
			slog.Error("Failed to parse Redis Cluster URL: ", err)
			return nil, err
		}
	} else {
		// init the redis client for write cache
		if opt, err := redis.ParseURL(redisUrl); err == nil {
			client.redisClient = redis.NewClient(opt)
		} else {
			slog.Error("Failed to parse Redis URL: ", err)
			return nil, err
		}
		// init the redis client for read cache
		if redisReaderUrl == "" {
			// if redisReaderUrl is empty, use redisUrl as redisReaderUrl
			redisReaderUrl = redisUrl
		}
		if opt, err := redis.ParseURL(redisReaderUrl); err == nil {
			client.redisClientReader = redis.NewClient(opt)
		} else {
			slog.Error("Failed to parse Redis Reader URL: ", err)
			return nil, err
		}
	}
	// Setup OTEL tracing
	if err := redisotel.InstrumentTracing(client.redisClient); err != nil {
		slog.Error("Failed to instrument tracing Redis Client: ", err)
	}
	if err := redisotel.InstrumentTracing(client.redisClientReader); err != nil {
		slog.Error("Failed to instrument tracing Redis Client Reader: ", err)
	}
	client.tracer = otel.Tracer("cache-service")
	// TODO: Setup OTEL metrics
	if groupcacheUrls != "" {
		// split the groupcacheUrls string into a slice of string by comma
		// ex: http://192.168.1.1:8080, http://192.168.1.2:8080, http://192.168.1.3:8080
		// for self single node setup, groupcacheUrls will be http://127.0.0.1:8080
		groupcacheUrlsSlice := strings.Split(groupcacheUrls, ",")
		// the first groupcacheUrl will be this instance groupcache server address
		groupcacheUrl := groupcacheUrlsSlice[0]
		// Pool keeps track of peers in our cluster and identifies which peer owns a key.
		pool := groupcache.NewHTTPPoolOpts(groupcacheUrl, &groupcacheOptions.poolOptions)

		if groupcacheOptions.disableAutoDiscovery {
			pool.Set(groupcacheUrlsSlice...)
		} else {
			// add these nodes to the rediscaches.groupcache.cluster set save in REDIS
			// convert string list to interface list
			members := make([]interface{}, len(groupcacheUrlsSlice))
			for i, v := range groupcacheUrlsSlice {
				members[i] = v
			}
			client.redisClient.SAdd(ctx, GroupCacheClusterNodesUrlKey, members)
			// Add more peers to the cluster You MUST Ensure our instance is included in this list else
			// determining who owns the key accross the cluster will not be consistent, and the pool won't
			// be able to determine if our instance owns the key.
			// get the urls from REDIS rediscaches.groupcache.cluster set
			groupcacheNodeUrls, err := client.redisClient.SMembers(ctx, GroupCacheClusterNodesUrlKey).Result()
			if err != nil {
				slog.Error("Error when get groupcache cluster nodes from REDIS: ", err)
				// use the input groupcacheUrlsSlice instead of cluster nodes from REDIS
				pool.Set(groupcacheUrlsSlice...)
			} else {
				// check if the current groupcacheUrl is in the groupcacheNodeUrls
				// if not, add it to the groupcacheNodeUrls
				if !slices.Contains(groupcacheNodeUrls, groupcacheUrl) {
					groupcacheNodeUrls = append(groupcacheNodeUrls, groupcacheUrl)
				}
				pool.Set(groupcacheNodeUrls...)
			}
			// Start a loop to keep updating the groupcache cluster nodes every second
			go func() {
				currentGroupcacheNodeUrls := groupcacheNodeUrls
				for {
					// check ctx
					select {
					case <-ctx.Done():
						return
					default:
						// sleep for 1 seconds
						time.Sleep(1 * time.Second)
						// get the urls from REDIS rediscaches.groupcache.cluster set
						pipe := client.redisClientReader.Pipeline()
						pipe.SAdd(ctx, GroupCacheClusterNodesUrlKey, groupcacheUrl)
						cmdSMembers := pipe.SMembers(ctx, GroupCacheClusterNodesUrlKey)
						pipe.Exec(ctx)
						// groupcacheNodeUrls, err := client.redisClientReader.SMembers(ctx, GroupCacheClusterNodesUrlKey).Result()
						groupcacheNodeUrls, err := cmdSMembers.Result()
						if err != nil {
							// check if err is client closed
							if err == redis.ErrClosed {
								// if client is already closed, exit the loop
								return
							}
							slog.Error("Error when get groupcache cluster nodes from REDIS: ", err)
						} else {
							// check if groupcache node url list is updated or not
							if !slices.Equal(currentGroupcacheNodeUrls, groupcacheNodeUrls) {
								slog.Info("Updating groupcache cluster with new nodes", "length", len(groupcacheNodeUrls))
								pool.Set(groupcacheNodeUrls...)
								currentGroupcacheNodeUrls = groupcacheNodeUrls
							}
						}
					}
				}
			}()
		}

		client.groupcacheServerUrl = groupcacheUrl
		serverAddr := strings.SplitN(groupcacheUrl, "://", 2)[1]
		serverPort := strings.SplitN(serverAddr, ":", 2)[1]
		// set regex when search for node url in error message
		groupcacheNodeUrlRegex = regexp.MustCompile(`\d+\.\d+\.\d+\.\d+:` + serverPort)
		client.groupcacheServer = http.Server{
			Addr:         serverAddr,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  60 * time.Second,
			Handler:      otelhttp.NewHandler(pool, "/_groupcache/"+GroupCacheName+"/*", otelhttp.WithServerName(serverAddr)),
		}
		// Start a HTTP server to listen for peer requests from the groupcache
		go func() {
			slog.Info("Starting groupcache server", "serverAddr", serverAddr)
			if err := client.groupcacheServer.ListenAndServe(); err != nil {
				log.Fatal(err)
			}
		}()

		// Create a new group cache with a max cache size
		client.groupcacheClient = groupcache.NewGroup(GroupCacheName, groupcacheOptions.cacheMaxSize, groupcache.GetterFunc(
			func(_ctx context.Context, cacheKey string, dest groupcache.Sink) error {
				getterFuncSpanCtx, span := client.tracer.Start(_ctx, "groupcache.GetterFunc",
					oteltrace.WithSpanKind(oteltrace.SpanKindInternal),
					oteltrace.WithAttributes(
						attribute.String("cache.key", cacheKey),
						attribute.String("groupcache.server_url", client.groupcacheServerUrl),
					))
				defer span.End()
				// This GetterFunc will be invoked when the cacheKey is not found locally in the current groupcache node
				// groupcache will try to invoke this only once (not guaranteed) if there are multiple
				// requests with the same cacheKey at the same time. (avoid thundering herd problem)
				// this function is guaranteed to be invoked on a same node with the same cacheKey. (please refer to doc)
				// In Graphql Engine Plus, this ussualy got invoke twice: the first call acquired a lock and
				// return redis.Nil then try to populate cache with the response data from the Hasura GraphQL Engine
				// the second call will wait for the lock to be released and get the cache data from redis
				slog.Debug("GetterFunc is called for cache key", "cacheKey", cacheKey)
				// Try to get the cache data from redis
				// if not found, redis.Nil error will be return
				// if found, groupcache will be populated with the cache data
				// we use pipeline to get the cache data and its TTL at the same time.
				var pipe redis.Pipeliner
				if groupcacheOptions.waitETA > 0 {
					// there will be a SetNX command to be executed
					pipe = client.redisClient.Pipeline()
				} else {
					// all readonly commands, so we can use the reader client
					pipe = client.redisClientReader.Pipeline()
				}
				getCmd := pipe.Get(getterFuncSpanCtx, cacheKey)
				ttlCmd := pipe.TTL(getterFuncSpanCtx, cacheKey)
				var setEtaCmd *redis.BoolCmd
				var defaultEta int64
				if groupcacheOptions.waitETA > 0 {
					// if waitETA >0, we will try to do ETA lock to wait for the cache data to be populated
					setEtaCmd = pipe.SetNX(getterFuncSpanCtx, GroupCacheName+":"+cacheKey+".ETA",
						time.Now().Add(time.Duration(groupcacheOptions.waitETA)*time.Millisecond).UnixMilli(),
						time.Duration(groupcacheOptions.waitETA)*time.Millisecond)
					defaultEta = time.Now().Add(time.Duration(groupcacheOptions.waitETA) * time.Millisecond).UnixMilli()
				}
				pipe.Exec(getterFuncSpanCtx)
				cacheData, originalErr := getCmd.Bytes()
				// originalErr can be redis.Nil or other error (redis server issue or network error)
				if originalErr != nil {
					// If waitETH is enabled and we got a cache miss.
					if groupcacheOptions.waitETA > 0 && originalErr == redis.Nil {
						// cache miss, check if ETA unix timestamp is set
						etaIsSet, _ := setEtaCmd.Result()
						if etaIsSet {
							// ETA is set, return redis.Nil right away to indicate cache miss and continue.
							// this current process has already acquired the lock to populate the cache data to this cache key
							// other process will wait for the ETA to expire before try to populate the cache data.
							return &groupcache.ErrNotFound{Msg: "cache key not found in redis (lock acquired)"}
						} else {
							// ETA is not set, this mean another process is populating
							// the cache data for this cache key. We should wait for a while and try again.
							// First get the ETA value
							eta, err := client.redisClientReader.Get(getterFuncSpanCtx, GroupCacheName+":"+cacheKey+".ETA").Int64()
							if err != nil {
								// there is an error when get ETA, set ETA to the defaultETA
								slog.Error("Error when get the cache key wait ETA from REDIS: ", err)
								eta = defaultEta
							}
							// Wait loop check til ETA
							waitSpanCtx, waitETALoopSpan := client.tracer.Start(getterFuncSpanCtx, "wait-loop",
								oteltrace.WithSpanKind(oteltrace.SpanKindInternal),
								oteltrace.WithAttributes(
									attribute.Int64("wait_duration_ms", eta-time.Now().UnixMilli()),
								))
							defer waitETALoopSpan.End()
							for time.Now().UnixMilli() < eta {
								// check for cache data is populated
								pipe := client.redisClientReader.Pipeline()
								checkCmd := pipe.Get(waitSpanCtx, cacheKey)
								getTTLCmd := pipe.TTL(waitSpanCtx, cacheKey)
								pipe.Exec(waitSpanCtx)
								cacheData, err := checkCmd.Bytes()
								if err == redis.Nil {
									// cache data is not populated yet, wait for 5 millisecond
									time.Sleep(5 * time.Millisecond)
									// check if getter function ctx is cancelled after sleep
									if getterFuncSpanCtx.Err() != nil {
										return getterFuncSpanCtx.Err()
									}
									continue
								} else if err != nil {
									// break out of loop to return original err
									slog.Error("Error when wait for cache to be populated: ", err)
									break
								} else {
									// cache data is populated, set the cache data to groupcache
									ttl := getTTLCmd.Val()
									// ttl returned from redis-go is already a duration, we dont need to multiply it by time.Second
									// Integer reply: TTL in seconds.
									// Integer reply: -1 if the key exists but has no associated expiration.
									// Integer reply: -2 if the key does not exist.
									expire := time.Time{} // Default to no expiry
									if ttl == -1 || ttl > 0 {
										if ttl > 0 {
											expire = time.Now().Add(time.Duration(ttl))
										}
										// must always call this function if return value is nil
										return dest.SetBytes(cacheData, expire)
									}
								}
							}
							if debugMode {
								// This is not really an error. If timeout is reached, the caller will proceed to retrieve the data from its source.
								slog.Error("Timeout while wait for cache key to be populated", "cacheKey", cacheKey, "eta", eta)
							}
						}
					}
					if originalErr == redis.Nil {
						// ErrNotFound should be returned from an implementation of `GetterFunc` to indicate the
						// requested value is not available. When remote HTTP calls are made to retrieve values from
						// other groupcache instances, returning this error will indicate to groupcache that the
						// value requested is not available, and it should NOT attempt to call `GetterFunc` locally.
						return &groupcache.ErrNotFound{Msg: "cache key not found in redis"}
					}
					return originalErr
				}
				ttl := ttlCmd.Val()
				// ttl returned from redis-go is already a duration, we dont need to multiply it by time.Second
				expire := time.Time{} // Default to no expiry
				if ttl > 0 {
					expire = time.Now().Add(time.Duration(ttl))
				}
				// must always call this function if return value is nil
				return dest.SetBytes(cacheData, expire)
			},
		))
	}
	return client, nil
}

// cache get using available client
func (client *RedisCacheClient) Get(ctx context.Context, key string) ([]byte, error) {
	// start tracer span
	spanCtx, span := client.tracer.Start(ctx, "RedisCacheClient.Get",
		oteltrace.WithSpanKind(oteltrace.SpanKindInternal),
		oteltrace.WithAttributes(
			attribute.String("cache.key", key),
		))
	defer span.End() // end tracer span
	if client.groupcacheClient != nil {
		// if groupcache is enabled, use groupcacheClient
		// groupcacheClient will return cache data if it is already be populated in the groupcache
		// if not, groupcacheClient will call the groupcache.GetterFunc to get the cache data from redis
		// if not found, redis.Nil error will be return.
		var cacheData []byte
		_, spanGroupcache := client.tracer.Start(spanCtx, "groupcache.Get",
			oteltrace.WithSpanKind(oteltrace.SpanKindInternal),
			oteltrace.WithAttributes(
				attribute.String("cache.key", key),
				attribute.String("groupcache.server_url", client.groupcacheServerUrl),
			))
		err := client.groupcacheClient.Get(spanCtx, key, groupcache.AllocatingByteSliceSink(&cacheData))
		spanGroupcache.End()
		if err != nil {
			span.SetAttributes(attribute.Bool("cache.hit", false))
			if debugMode {
				// only print this log in debug mode
				slog.Error("Error when groupcache.Get: ", err)
			}
			return nil, err
		}
		span.SetAttributes(attribute.Bool("cache.hit", true))
		return cacheData, nil
	} else {
		// if groupcache is not enabled, use redisClient
		cacheData, err := client.redisClientReader.Get(spanCtx, key).Bytes()
		if err != nil {
			span.SetAttributes(attribute.Bool("cache.hit", false))
			return nil, err
		}
		span.SetAttributes(attribute.Bool("cache.hit", true))
		return cacheData, nil
	}
}

// cache set using available client
// the value must be encoded to []byte before calling this function
func (client *RedisCacheClient) Set(ctx context.Context, key string, value []byte, ttl int) error {
	// start tracer span
	spanCtx, span := client.tracer.Start(ctx, "RedisCacheClient.Set",
		oteltrace.WithSpanKind(oteltrace.SpanKindInternal),
		oteltrace.WithAttributes(
			attribute.String("cache.key", key),
			attribute.Int("cache.ttl", ttl),
			attribute.String("groupcache.server_url", client.groupcacheServerUrl),
		))
	defer span.End() // end tracer span
	if !(ttl == -1 || ttl > 0) {
		return fmt.Errorf("invalid TTL, value must be -1 or greater than 0. Actual value: %d", ttl)
	}
	// set the cache data to redis
	// Default to no expiry
	expiration := time.Duration(0)
	if ttl > 0 {
		expiration = time.Duration(ttl) * time.Second
	}
	err := client.redisClient.Set(spanCtx, key, value, expiration).Err()
	if err != nil {
		// If unable to set the cache data to redis, return the error right away
		// We are not going to save to groupcache if redis set failed
		return err
	}
	// if groupcache is enabled, use it to set the cache data
	if client.groupcacheClient != nil {
		// Default to no expiry
		_, spanGroupcache := client.tracer.Start(spanCtx, "groupcache.Set",
			oteltrace.WithSpanKind(oteltrace.SpanKindInternal),
			oteltrace.WithAttributes(
				attribute.String("cache.key", key),
				attribute.Int("cache.ttl", ttl),
				attribute.String("groupcache.server_url", client.groupcacheServerUrl),
			))
		expire := time.Time{}
		if ttl > 0 {
			expire = time.Now().Add(time.Duration(ttl) * time.Second)
		}
		if err := client.groupcacheClient.Set(spanCtx, key, value, expire, false); err != nil {
			slog.Error("Error when groupcache.Set: ", err)
			// detect if err message contain groupcacheNodeUrlRegex
			failedNodeAddr := groupcacheNodeUrlRegex.FindString(err.Error())
			if failedNodeAddr != "" {
				if err := client.redisClient.SRem(spanCtx, GroupCacheClusterNodesUrlKey, "http://"+failedNodeAddr).Err(); err != nil {
					slog.Error("Error when remove failed groupcache node url from REDIS: ", err)
				}
			}
		}
		spanGroupcache.End()
	}
	return err
}

func (client *RedisCacheClient) Close(shutdownCtx context.Context, withGroupcacheServer bool) error {
	// close redis client
	if client.groupcacheServerUrl != "" {
		if err := client.redisClient.SRem(shutdownCtx, GroupCacheClusterNodesUrlKey, client.groupcacheServerUrl).Err(); err != nil {
			slog.Error("Error when remove groupcache server url from REDIS: ", err)
		}
	}
	err1 := client.redisClient.Close()
	err2 := client.redisClientReader.Close()
	if client.groupcacheServerUrl != "" && withGroupcacheServer {
		// If fiber http server is already closed, http.Server.Close() will call os.Exit(1)
		// and will not print the below error message
		err := client.groupcacheServer.Shutdown(shutdownCtx)
		if err != nil {
			slog.Error("Error when groupcache httpServer.Shutdown(): ", err)
		}
	}
	// we dont need to close groupcache client as it is not a connection pool
	if err1 != nil {
		return err1
	} else {
		return err2
	}
}
