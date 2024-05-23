package main

import (
	"os"
	"strconv"
)

// The server Host name to listen on
// default to empty string (equivalent to 0.0.0.0) if the env is not set
var engineServerHost = os.Getenv("ENGINE_PLUS_SERVER_HOST")

// The server port to listen on
// default to 8000 if the env is not set
var engineServerPort = os.Getenv("ENGINE_PLUS_SERVER_PORT")

// The API PATH for the read-write queries
// default to /public/graphql/v1 if the env is not set
var rwPath = os.Getenv("ENGINE_PLUS_GRAPHQL_V1_PATH")

// The API PATH for the readonly queries
// default to /public/graphql/v1readonly if the env is not set
// request send to this path will be routed between primary and replica
var roPath = os.Getenv("ENGINE_PLUS_GRAPHQL_V1_READONLY_PATH")

// The API PATH for the scripting engine
// If not set, no public endpoint will be exposed to send request to scripting engine
// scripting engine can still be used in hasura metadata by using the local endpoint
// at http://localhost:8880/execute
// or at http://localhost:8880/validate
// For ex, If set engineMetaPath = "/scripting", the following endpoint will be exposed:
// POST /scripting/upload -> /upload : This endpoint allow upload scripts to the scripting engine
// POST /scripting/execute -> /execute : This endpoint allow to execute a script using scripting engine
var scriptingPublicPath = os.Getenv("ENGINE_PLUS_SCRIPTING_PUBLIC_PATH")

// The secret key to authenticate the request to the scripting engine
// Must be set if the scriptingPublicPath is set
var envExecuteSecret = os.Getenv("ENGINE_PLUS_EXECUTE_SECRET")

// Set to true to enable execute from url for the scripting engine
var allowExecuteUrl, _ = strconv.ParseBool(os.Getenv("ENGINE_PLUS_ALLOW_EXECURL"))

// The API PATH for the health check endpoint. This endpoint will do health checks
// of all dependent services, ex: Hasura graphql-engine, Python scripting-engine, etc.
// default to /public/graphql/health if the env is not set
var healthCheckPath = os.Getenv("ENGINE_PLUS_HEALTH_CHECK_PATH")

// The API PATH to enable schema migrations, console, etc.
// default to /public/meta if the env is not set
var engineMetaPath = os.Getenv("ENGINE_PLUS_META_PATH")

// The weight to route request between primary and replica
// The value must be between 0 and 100. 100 mean all queries will go to primary
// 0 mean all queries will go to replica. The default value is 50.
// This value is only used if the HASURA_GRAPHQL_READ_REPLICA_URLS is set
var engineGqlPvRweight, engineGqlPvRweightParseErr = strconv.ParseUint(os.Getenv("ENGINE_PLUS_GRAPHQL_PRIMARY_VS_REPLICA_WEIGHT"), 10, 64)

// Set to "http" or "grpc" to enable Open Telemetry tracing
// "http" will use the HTTP exporter which sent trace to https://localhost:4318/v1/traces.
// to customise please see this: https://pkg.go.dev/go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp
// "grpc" will use the gRPC exporter which sent trace to https://localhost:4317.
// to customise please see this: https://pkg.go.dev/go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc
// default to empty string if the env is not set. (Disable Open Telemetry)
var engineEnableOtelType = os.Getenv("ENGINE_PLUS_ENABLE_OPEN_TELEMETRY")

// Set to "true" to enable debug mode
var debugMode, _ = strconv.ParseBool(os.Getenv("DEBUG"))

// The port to be used by the groupcache server to enhance the query caching
// default to 8879 if the env is not set
var groupcacheServerPort = os.Getenv("ENGINE_PLUS_GROUPCACHE_PORT")

// The cluster mode to be used by the groupcache server for automatic discovery
// There are 2 available modes:
// 1. "aws_ec2": This mode will use the AWS EC2 metadata to get the local ipv4 address
// 2. "host_net": This mode will use the current host network interfaces to get the local ipv4 address
// if not set, auto discovery will be disabled and the groupcache server will use 127.0.0.1 as the server address
var engineGroupcacheClusterMode = os.Getenv("ENGINE_PLUS_GROUPCACHE_CLUSTER_MODE")

// The maximum wait time for the groupcache client to wait for the cache owner to populate the cache data
// by seting it > 0, the others process will wait up to engineGroupcacheWaitEta seconds for
// the first process (cache owner) to populate the cache data to redis and then the others process will
// retrieve the data from the redis cache once it is already populated.
// this feature will help avoid the thundering herd problem.
// value can not be set greater than to 3000 (miliseconds).
// default to 500 (miliseconds) if the env is not set
// Note: can set to 0 to disable this feature, this give the cache the best performance.
// Recommended to always leave this on even when engineGroupcacheClusterMode is not set.
// Only disable it when your setup always have only 1 node running.
var engineGroupcacheWaitEta, engineGroupcacheWaitEtaParseErr = strconv.ParseUint(os.Getenv("ENGINE_PLUS_GROUPCACHE_WAIT_ETA"), 10, 64)

// The maximum memory can be used for internal memory groupcache
// The value is in bytes, default to 60000000 (60Mib) if not set
var engineGroupcacheMaxSize, engineGroupcacheMaxSizeParseErr = strconv.ParseUint(os.Getenv("ENGINE_PLUS_GROUPCACHE_MAX_SIZE"), 10, 64)

// Below are the environment variables that are also used by Hasura GraphQL Engine

// The CORS domain to allow requests from. By default we enable CORS for all domain.
// default to "*" if the env is not set
var hasuraGqlCorsDomain = os.Getenv("HASURA_GRAPHQL_CORS_DOMAIN")

// The database URL for the replica database
// If this env is set, the server will start another Hasura GraphQL Engine server
// which configure to the read-replica database. The engine plus will check if the
// graphql query is a read-only query, it will route the query to the both
// primary & replica server using the weight defined in enviroment
// if the query is a write query, it will only route to the primary server
// default to empty string if the env is not set
// In a multiple databases setup, you need to pass each database url in a comma
// separated string, follow this format: ENV_KEY1=DB_URL1,ENV_KEY2=DB_URL2,...
// where the ENV_KEY1 represent the environment variable name to configure for DB1
var hasuraGqlReadReplicaUrls = os.Getenv("HASURA_GRAPHQL_READ_REPLICA_URLS")

// The database URL for the primary database
// In a multiple databases setup, this env doesnt need to be set. Instead
// You can configure the DB URL for each database using a custom environment variable
// For example: ENV_KEY1=DB_URL1,ENV_KEY2=DB_URL2,...
var hasuraGqlDatabaseUrl = os.Getenv("HASURA_GRAPHQL_DATABASE_URL")

// The database URL for the metadata database
// If this env is not set, the server will use the primary database url hasuraGqlDatabaseUrl
var hasuraGqlMetadataDatabaseUrl = os.Getenv("HASURA_GRAPHQL_METADATA_DATABASE_URL")

// The jwt secret config for the Hasura GraphQL Engine
var hasuraGqlJwtSecret = os.Getenv("HASURA_GRAPHQL_JWT_SECRET")

// The redis URL used by GraphQL Engine Plus for query caching
// This env is used by Hasura Cloud only, not the CE version
var hasuraGqlRedisUrl = os.Getenv("HASURA_GRAPHQL_REDIS_URL")

// The redis URL used by GraphQL Engine Plus for query caching (read only)
// Note: Hasura Cloud may not use this env
var hasuraGqlRedisReaderUrl = os.Getenv("HASURA_GRAPHQL_REDIS_READER_URL")

// The redis cluster URL used by GraphQL Engine Plus for query caching
// Note: Hasura Cloud  may not use this env
var hasuraGqlRedisClusterUrl = os.Getenv("HASURA_GRAPHQL_REDIS_CLUSTER_URL")

// The maximum allowed cache TTL for a query
// If the query has ttl value greater than this max value, the cache will use this max value
// Default to 3600 seconds if the env is not set (same as Hasura Cloud)
// Set this value to 0 will disable query caching
var hasuraGqlCacheMaxEntryTtl, hasuraGqlCacheMaxEntryTtlParseErr = strconv.ParseUint(os.Getenv("HASURA_GRAPHQL_CACHE_MAX_ENTRY_TTL"), 10, 64)

func InitConfig() {
	// Init default value for API PATH defined in the environment variable
	if rwPath == "" {
		rwPath = "/public/graphql/v1"
	}
	if roPath == "" {
		roPath = "/public/graphql/v1readonly"
	}
	if healthCheckPath == "" {
		healthCheckPath = "/public/graphql/health"
	}
	if engineMetaPath == "" {
		engineMetaPath = "/public/meta"
	} else if engineMetaPath[len(engineMetaPath)-1:] == "/" {
		// check if metadataPath end with /
		// if yes, remove / from the end of metadataPath.
		engineMetaPath = engineMetaPath[:len(engineMetaPath)-1]
	}
	// all path must not end with "/"
	if scriptingPublicPath[len(scriptingPublicPath)-1:] == "/" {
		// check if scriptingPublicPath end with /
		// if yes, remove / from the end of metadataPath.
		scriptingPublicPath = scriptingPublicPath[:len(scriptingPublicPath)-1]
	}

	// Init default value for CORS domain
	if hasuraGqlCorsDomain == "" {
		hasuraGqlCorsDomain = "*"
	}
	// Init default value for primary vs replica weight routing
	if engineGqlPvRweightParseErr != nil {
		engineGqlPvRweight = 50
	}
	// Init default value for graphql query caching max TTL
	if hasuraGqlCacheMaxEntryTtlParseErr != nil {
		hasuraGqlCacheMaxEntryTtl = 3600
	}
	// Init default value for groupcache waitETA
	if engineGroupcacheWaitEtaParseErr != nil {
		engineGroupcacheWaitEta = 500
	} else {
		// limit the max value to 3000, this allow the group wait to loop for ~500-600 times
		// each loop will sleep for 5 miliseconds before check the redis cache again
		if engineGroupcacheWaitEta > 3000 {
			engineGroupcacheWaitEta = 3000
		}
	}
	// Init default value for groupcache cacheMaxSize
	if engineGroupcacheMaxSizeParseErr != nil {
		engineGroupcacheMaxSize = 60000000
	}
}
