package main

import (
	"context"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/mailgun/groupcache/v2"
	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
)

type RedisCacheClient struct {
	redisClient         redis.UniversalClient
	redisClientReader   redis.UniversalClient
	groupcacheClient    *groupcache.Group
	groupcacheServerUrl string
}

var groupcacheServer http.Server

const GroupCacheClusterNodesUrlKey = "rediscaches.groupcache.cluster"

type GroupcacheOptions struct {
	// default to 60MiB
	cacheMaxSize int64
	// default to 300 seconds
	defaultTTL uint64

	// by seting it > 0, the others process will wait up to ETA seconds for first process to populate the cache data
	// this will avoid the thundering herd problem. value default to 3 seconds.
	waitETA     uint64
	poolOptions groupcache.HTTPPoolOptions
}

func NewGroupCacheOptions() (options GroupcacheOptions) {
	options.cacheMaxSize = 60000000
	options.defaultTTL = 300
	options.waitETA = 3
	return
}

func NewRedisCacheClient(ctx context.Context, redisUrl string, redisReaderUrl string, groupcacheUrls string, groupcacheOptions GroupcacheOptions) (*RedisCacheClient, error) {
	client := &RedisCacheClient{}
	// check if redisUrl is started with rediss://
	if strings.HasPrefix(redisUrl, "rediss://") {
		// remove unexpected option from clusterUrl using regex
		clusterUrl := redisUrlRemoveUnexpectedOptionRegex.ReplaceAllString(redisUrl, "$1")
		if clusterOpt, err := redis.ParseClusterURL(clusterUrl); err == nil {
			clusterOpt.TLSConfig.InsecureSkipVerify = true
			client.redisClient = redis.NewClusterClient(clusterOpt)
		} else {
			log.Print("Failed to parse Redis Cluster URL: ", err)
			return nil, err
		}
	} else {
		if opt, err := redis.ParseURL(redisUrl); err == nil {
			client.redisClient = redis.NewClient(opt)
		} else {
			log.Print("Failed to parse Redis URL: ", err)
			return nil, err
		}
	}
	// Enable tracing instrumentation.
	if err := redisotel.InstrumentTracing(client.redisClient); err != nil {
		log.Print("Failed to instrument tracing Redis Client: ", err)
	}
	// Enable metrics instrumentation.
	if err := redisotel.InstrumentMetrics(client.redisClient); err != nil {
		log.Print("Failed to instrument metrics Redis Client: ", err)
	}

	// do the same for redisReaderUrl
	if redisReaderUrl == "" {
		// if redisReaderUrl is empty, use redisUrl as redisReaderUrl
		redisReaderUrl = redisUrl
	}
	if strings.HasPrefix(redisReaderUrl, "rediss://") {
		// remove unexpected option from clusterUrl using regex
		clusterUrl := redisUrlRemoveUnexpectedOptionRegex.ReplaceAllString(redisReaderUrl, "$1")
		if clusterOpt, err := redis.ParseClusterURL(clusterUrl); err == nil {
			clusterOpt.TLSConfig.InsecureSkipVerify = true
			clusterOpt.RouteByLatency = true
			client.redisClientReader = redis.NewClusterClient(clusterOpt)
		} else {
			log.Print("Failed to parse Redis Cluster URL: ", err)
			return nil, err
		}
	} else {
		if opt, err := redis.ParseURL(redisReaderUrl); err == nil {
			client.redisClientReader = redis.NewClient(opt)
		} else {
			log.Print("Failed to parse Redis URL: ", err)
			return nil, err
		}
	}
	// Enable tracing instrumentation.
	if err := redisotel.InstrumentTracing(client.redisClientReader); err != nil {
		log.Print("Failed to instrument tracing Redis Client Reader: ", err)
	}
	// Enable metrics instrumentation.
	if err := redisotel.InstrumentMetrics(client.redisClientReader); err != nil {
		log.Print("Failed to instrument metrics Redis Client Reader: ", err)
	}

	if groupcacheUrls != "" {
		// split the groupcacheUrls string into a slice of string by comma
		// ex: http://192.168.1.1:8080, http://192.168.1.2:8080, http://192.168.1.3:8080
		// for self single node setup, groupcacheUrls will be http://127.0.0.1:8080
		groupcacheUrlsSlice := strings.Split(groupcacheUrls, ",")
		// add these nodes to the rediscaches.groupcache.cluster set save in REDIS
		// convert string list to interface list
		members := make([]interface{}, len(groupcacheUrlsSlice))
		for i, v := range groupcacheUrlsSlice {
			members[i] = v
		}
		client.redisClient.SAdd(ctx, GroupCacheClusterNodesUrlKey, members)
		// the first groupcacheUrl will be this instance groupcache server address
		groupcacheUrl := groupcacheUrlsSlice[0]
		// Pool keeps track of peers in our cluster and identifies which peer owns a key.
		pool := groupcache.NewHTTPPoolOpts(groupcacheUrl, &groupcacheOptions.poolOptions)
		// Add more peers to the cluster You MUST Ensure our instance is included in this list else
		// determining who owns the key accross the cluster will not be consistent, and the pool won't
		// be able to determine if our instance owns the key.
		// get the urls from REDIS rediscaches.groupcache.cluster set
		groupcacheNodeUrls, err := client.redisClient.SMembers(ctx, GroupCacheClusterNodesUrlKey).Result()
		if err != nil {
			log.Println("Error when get groupcache cluster nodes from REDIS: ", err)
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
		client.groupcacheServerUrl = groupcacheUrl
		serverAddr := strings.SplitN(groupcacheUrl, "://", 2)[1]
		groupcacheServer = http.Server{
			Addr:    serverAddr,
			Handler: pool,
		}
		// Start a HTTP server to listen for peer requests from the groupcache
		go func() {
			log.Printf("Groupcache server listening on %s\n", serverAddr)
			if err := groupcacheServer.ListenAndServe(); err != nil {
				log.Fatal(err)
			}
		}()

		// Create a new group cache with a max cache size
		client.groupcacheClient = groupcache.NewGroup("rediscaches", groupcacheOptions.cacheMaxSize, groupcache.GetterFunc(
			func(ctx context.Context, cacheKey string, dest groupcache.Sink) error {
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
				getCmd := pipe.Get(ctx, cacheKey)
				ttlCmd := pipe.TTL(ctx, cacheKey)
				var setEtaCmd *redis.BoolCmd
				if groupcacheOptions.waitETA > 0 {
					// if waitETA >0, we will try to do ETA lock to wait for the cache data to be populated
					setEtaCmd = pipe.SetNX(ctx, cacheKey+"-rediscaches.ETA",
						time.Now().Add(time.Duration(groupcacheOptions.waitETA)*time.Second).UnixMilli(),
						time.Duration(groupcacheOptions.waitETA)*time.Second)
				}
				pipe.Exec(ctx)
				cacheData, originalErr := getCmd.Bytes()
				if originalErr != nil {
					if groupcacheOptions.waitETA > 0 && originalErr == redis.Nil {
						// cache miss, check if ETA is set
						etaIsSet, _ := setEtaCmd.Result()
						if etaIsSet {
							// ETA is set, return redis.Nil right away to indicate cache miss and continue.
							// this current process has already acquired the lock to populate the cache data to this cache key
							// other process will wait for the ETA to expire before try to populate the cache data.
							return redis.Nil
						} else {
							// ETA is not set, this mean another process is populating
							// the cache data for this cache key. We should wait for a while and try again.
							// First get the ETA value
							eta, err := client.redisClientReader.Get(ctx, cacheKey+"-rediscaches.ETA").Int64()
							if err != nil {
								eta = time.Now().Add(time.Duration(groupcacheOptions.waitETA) * time.Second).UnixMilli()
							}
							// Wait loop check til ETA
							for time.Now().UnixMilli() < eta {
								// check for cache data is populated
								pipe := client.redisClientReader.Pipeline()
								checkCmd := pipe.Get(ctx, cacheKey)
								getTTLCmd := pipe.TTL(ctx, cacheKey)
								pipe.Exec(ctx)
								cacheData, err := checkCmd.Bytes()
								if err == redis.Nil {
									// cache data is not populated yet, wait for 50 millisecond
									time.Sleep(50 * time.Millisecond)
									continue
								} else if err != nil {
									// break out of loop to return original err
									log.Println("Error when wait for cache to be populated: ", err)
									break
								} else {
									// cache data is populated, set the cache data to groupcache
									ttl := getTTLCmd.Val()
									if ttl < 0 {
										ttl = time.Duration(groupcacheOptions.defaultTTL) * time.Second
									}
									return dest.SetBytes(cacheData, time.Now().Add(ttl))
								}
							}
						}
					}
					return originalErr
				}
				ttl := ttlCmd.Val()
				if ttl < 0 {
					ttl = time.Duration(groupcacheOptions.defaultTTL) * time.Second
				}
				// Set cache data in the groupcache to expire after TTL seconds
				// if TTL is less than 0, set it to defaultTTL
				return dest.SetBytes(cacheData, time.Now().Add(ttl))
			},
		))
	}
	return client, nil
}

// cache get using available client
func (client *RedisCacheClient) Get(ctx context.Context, key string, traceOpts TraceOptions) ([]byte, error) {
	// start tracer span
	_, span := traceOpts.tracer.Start(traceOpts.ctx, "RedisCacheClient.Get",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
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
		err := client.groupcacheClient.Get(ctx, key, groupcache.AllocatingByteSliceSink(&cacheData))
		if err != nil {
			span.SetAttributes(attribute.Bool("cache.hit", false))
			return nil, err
		}
		span.SetAttributes(attribute.Bool("cache.hit", true))
		return cacheData, nil
	} else {
		// if groupcache is not enabled, use redisClient
		cacheData, err := client.redisClientReader.Get(ctx, key).Bytes()
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
func (client *RedisCacheClient) Set(ctx context.Context, key string, value []byte, expiration time.Duration, traceOpts TraceOptions) error {
	// start tracer span
	_, span := traceOpts.tracer.Start(traceOpts.ctx, "RedisCacheClient.Set",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("cache.key", key),
		))
	defer span.End() // end tracer span
	// if groupcache is enabled, use it to set the cache data
	if client.groupcacheClient != nil {
		// run this in a goroutine for faster response
		// cache will be set to redis any way
		go func() {
			// this will replicate cache data to all nodes in the groupcache cluster
			// to disable this behaviour, set the last parameter to false
			if err := client.groupcacheClient.Set(ctx, key, value, time.Now().Add(expiration), true); err != nil {
				log.Println("Error when groupcache.Set: ", err)
			}
		}()
	}
	// set the cache data to redis
	err := client.redisClient.Set(ctx, key, value, expiration).Err()
	return err
}

func (client *RedisCacheClient) Close(shutdownCtx context.Context, withGroupcacheServer bool) error {
	// close redis client
	if client.groupcacheServerUrl != "" {
		if err := client.redisClient.SRem(shutdownCtx, GroupCacheClusterNodesUrlKey, client.groupcacheServerUrl).Err(); err != nil {
			log.Println("Error when remove groupcache server url from REDIS: ", err)
		}
	}
	err1 := client.redisClient.Close()
	err2 := client.redisClientReader.Close()
	if client.groupcacheServerUrl != "" && withGroupcacheServer == true {
		// If fiber http server is already closed, http.Server.Close() will call os.Exit(1)
		// and will not print the below error message
		err := groupcacheServer.Shutdown(shutdownCtx)
		if err != nil {
			log.Println("Error when groupcache httpServer.Shutdown(): ", err)
		}
	}
	// we dont need to close groupcache client as it is not a connection pool
	if err1 != nil {
		return err1
	} else {
		return err2
	}
}
