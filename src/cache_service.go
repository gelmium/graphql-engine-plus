package main

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/mailgun/groupcache/v2"
	"github.com/redis/go-redis/v9"
)

type RedisCacheClient struct {
	redisClient       redis.UniversalClient
	redisReaderClient redis.UniversalClient
	groupcacheClient  *groupcache.Group
}

var groupcacheServer http.Server

type GroupcacheOptions struct {
	// default to 30MB
	cacheMaxSize int64
	// default to 300 seconds
	defaultTTL  uint64
	poolOptions groupcache.HTTPPoolOptions
}

func NewGroupCacheOptions() (options GroupcacheOptions) {
	options.cacheMaxSize = 30000000
	options.defaultTTL = 300
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
			client.redisReaderClient = redis.NewClusterClient(clusterOpt)
		} else {
			log.Print("Failed to parse Redis Cluster URL: ", err)
			return nil, err
		}
	} else {
		if opt, err := redis.ParseURL(redisReaderUrl); err == nil {
			client.redisReaderClient = redis.NewClient(opt)
		} else {
			log.Print("Failed to parse Redis URL: ", err)
			return nil, err
		}
	}

	if groupcacheUrls != "" {
		// split the groupcacheUrls string into a slice of string by comma
		// ex: http://192.168.1.1:8080, http://192.168.1.2:8080, http://192.168.1.3:8080
		// for self single node setup, groupcacheUrls will be http://127.0.0.1:8080
		groupcacheUrlsSlice := strings.Split(groupcacheUrls, ",")
		// the first groupcacheUrl will be this instance groupcache server address
		groupcacheUrl := groupcacheUrlsSlice[0]
		// Pool keeps track of peers in our cluster and identifies which peer owns a key.
		pool := groupcache.NewHTTPPoolOpts(groupcacheUrl, &groupcacheOptions.poolOptions)
		// Add more peers to the cluster You MUST Ensure our instance is included in this list else
		// determining who owns the key accross the cluster will not be consistent, and the pool won't
		// be able to determine if our instance owns the key.
		pool.Set(groupcacheUrlsSlice...)
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
				// if found, groupcache will be populated with the cache data.
				cacheData, err := client.redisReaderClient.Get(ctx, cacheKey).Bytes()
				if err != nil {
					return err
				}
				// Set cache data in the groupcache to expire after default TTL seconds
				return dest.SetBytes(cacheData, time.Now().Add(time.Duration(groupcacheOptions.defaultTTL)*time.Second))
			},
		))
	}
	return client, nil
}

// cache get using available client
func (client *RedisCacheClient) Get(ctx context.Context, key string) ([]byte, error) {
	if client.groupcacheClient != nil {
		// if groupcache is enabled, use groupcacheClient
		// groupcacheClient will return cache data if it is already be populated in the groupcache
		// if not, groupcacheClient will call the groupcache.GetterFunc to get the cache data from redis
		// if not found, redis.Nil error will be return.
		var cacheData []byte
		err := client.groupcacheClient.Get(ctx, key, groupcache.AllocatingByteSliceSink(&cacheData))
		if err != nil {
			return nil, err
		}
		return cacheData, nil
	} else {
		// if groupcache is not enabled, use redisClient
		return client.redisReaderClient.Get(ctx, key).Bytes()
	}
}

// cache set using available client
// the value must be encoded to []byte before calling this function
func (client *RedisCacheClient) Set(ctx context.Context, key string, value []byte, expiration time.Duration) error {
	// if groupcache is enabled, use it to set the cache data
	if client.groupcacheClient != nil {
		// run this in a goroutine for faster response
		// cache will be set to redis any way
		go func() {
			// this will replicate cache data to all nodes in the groupcache cluster
			// to disable this behaviour, set the last parameter to false
			client.groupcacheClient.Set(ctx, key, value, time.Now().Add(expiration), true)
		}()
	}
	// set the cache data to redis
	err := client.redisClient.Set(ctx, key, value, expiration).Err()
	return err
}

func (client *RedisCacheClient) Close(closeGroupcacheHttpServer bool) error {
	// close redis client
	err1 := client.redisClient.Close()
	err2 := client.redisReaderClient.Close()
	if closeGroupcacheHttpServer {
		// for some reason, http.Server.Close() may call os.Exit(1)
		// and will not print the below error message
		err := groupcacheServer.Close()
		if err != nil {
			log.Println("Error when close groupcache server: ", err)
		}
	}
	// we dont need to close groupcache client as it is not a connection pool
	if err1 != nil {
		return err1
	} else {
		return err2
	}
}
