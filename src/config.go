package main

import (
	"github.com/spf13/viper"
)

// The server host of Graphql Engine Plus to listen on
// default to empty string (equivalent to 0.0.0.0) if the env is not set
// this allow the server to listen on all interfaces
const ENGINE_PLUS_SERVER_HOST = "ENGINE_PLUS_SERVER_HOST"

// The server port of Graphql Engine Plus to listen on
// default to 8000 if the env is not set
const ENGINE_PLUS_SERVER_PORT = "ENGINE_PLUS_SERVER_PORT"

// The API PATH for the read-write queries
// default to /public/graphql/v1 if the env is not set
const ENGINE_PLUS_GRAPHQL_V1_PATH = "ENGINE_PLUS_GRAPHQL_V1_PATH"

// The API PATH for the readonly queries
// default to /public/graphql/v1readonly if the env is not set
// request send to this path will be routed between primary and replica
const ENGINE_PLUS_GRAPHQL_V1_READONLY_PATH = "ENGINE_PLUS_GRAPHQL_V1_READONLY_PATH"

// The secret key to authenticate the request to the scripting engine
// Must be set if the scriptingPublicPath is set
const ENGINE_PLUS_EXECUTE_SECRET = "ENGINE_PLUS_EXECUTE_SECRET"

// The API PATH for the scripting engine
// If not set, no public endpoint will be exposed to send request to scripting engine
// scripting engine can still be used in hasura metadata by using the local endpoint
// at http://localhost:8880/execute
// or at http://localhost:8880/validate
// For ex, If set ENGINE_PLUS_SCRIPTING_PUBLIC_PATH = "/scripting", the following endpoint will be exposed:
// POST /scripting/upload -> /upload : This endpoint allow upload scripts to the scripting engine
// POST /scripting/execute -> /execute : This endpoint allow to execute a script using scripting engine
// GET /scripting/validate -> /validate : This endpoint allow to validate a GraphQL POST using scripting engine
// GET /scripting/health/engine -> /health/engine : This endpoint check the health of the scripting engine
const ENGINE_PLUS_SCRIPTING_PUBLIC_PATH = "ENGINE_PLUS_SCRIPTING_PUBLIC_PATH"

// Set to true to enable execute from url for the scripting engine
const ENGINE_PLUS_ALLOW_EXECURL = "ENGINE_PLUS_ALLOW_EXECURL"

// The API PATH for the health check endpoint. This endpoint will do health checks
// of all dependent services, ex: Hasura graphql-engine, Python scripting-engine, etc.
// default to /public/graphql/health if the env is not set
const ENGINE_PLUS_HEALTH_CHECK_PATH = "ENGINE_PLUS_HEALTH_CHECK_PATH"

// The API PATH to enable schema migrations, console, etc.
// default to /public/meta if the env is not set
// this allow the user to access the metadata API of Hasura GraphQL Engine
// for ex: POST /public/meta/v1/metadata -> /v1/metadata
// for ex: POST /public/meta/v2/query -> /v2/query
// for ex: GET /public/meta/v1/version -> /v1/version
const ENGINE_PLUS_META_PATH = "ENGINE_PLUS_META_PATH"

// The weight to route request between primary and replica
// The value must be between 0 and 100. 100 mean all queries will go to primary
// 0 mean all queries will go to replica. The default value is 50.
// This value is only used if the HASURA_GRAPHQL_READ_REPLICA_URLS is set
const ENGINE_PLUS_GRAPHQL_PRIMARY_VS_REPLICA_WEIGHT = "ENGINE_PLUS_GRAPHQL_PRIMARY_VS_REPLICA_WEIGHT"

// Set to "http" or "grpc" to enable Open Telemetry tracing
// "http" will use the HTTP exporter which sent trace to https://localhost:4318/v1/traces.
// to customise please see this: https://pkg.go.dev/go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp
// "grpc" will use the gRPC exporter which sent trace to https://localhost:4317.
// to customise please see this: https://pkg.go.dev/go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc
// default to empty string if the env is not set. (Disable Open Telemetry)
const ENGINE_PLUS_ENABLE_OPEN_TELEMETRY = "ENGINE_PLUS_ENABLE_OPEN_TELEMETRY"

// Set to "true" to enable debug mode
const DEBUG_MODE = "DEBUG_MODE"

// The port to be used by the groupcache server to enhance the query caching
// default to 8879 if the env is not set
const ENGINE_PLUS_GROUPCACHE_PORT = "ENGINE_PLUS_GROUPCACHE_PORT"

// The cluster mode to be used by the groupcache server for automatic discovery
// There are 2 available modes:
// 1. "aws_ec2": This mode will use the AWS EC2 metadata to get the local ipv4 address
// 2. "host_net": This mode will use the current host network interfaces to get the local ipv4 address
// if not set, auto discovery will be disabled and the groupcache server will use 127.0.0.1 as the server address
const ENGINE_PLUS_GROUPCACHE_CLUSTER_MODE = "ENGINE_PLUS_GROUPCACHE_CLUSTER_MODE"

// The maximum wait time for the groupcache client to wait for the cache owner to populate the cache data
// by seting it > 0, the others process will wait up to ENGINE_PLUS_GROUPCACHE_WAIT_ETA (ms) for
// the first process (cache owner) to populate the cache data to redis and then the others process will
// retrieve the data from the redis cache once it is already populated.
// this feature will help avoid the thundering herd problem.
// value can not be set greater than to 3000 (miliseconds).
// default to 500 (miliseconds) if the env is not set
// Note: can set to 0 to disable this feature, this give the cache the best performance.
// Recommended to always leave this on even when ENGINE_PLUS_GROUPCACHE_CLUSTER_MODE is not set.
// Only disable it when your setup always have only 1 node running.
const ENGINE_PLUS_GROUPCACHE_WAIT_ETA = "ENGINE_PLUS_GROUPCACHE_WAIT_ETA"

// The maximum memory can be used for internal memory groupcache
// The value is in bytes, default to 60000000 (60Mib) if not set
const ENGINE_PLUS_GROUPCACHE_MAX_SIZE = "ENGINE_PLUS_GROUPCACHE_MAX_SIZE"

// *** Below are the environment variables that are also used by Hasura GraphQL Engine ***

// The CORS domain to allow requests from. By default we enable CORS for all domain.
// default to "*" if the env is not set
const HASURA_GRAPHQL_CORS_DOMAIN = "HASURA_GRAPHQL_CORS_DOMAIN"

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
const HASURA_GRAPHQL_READ_REPLICA_URLS = "HASURA_GRAPHQL_READ_REPLICA_URLS"

// The database URL for the primary database
// In a multiple databases setup, this env doesnt need to be set. Instead
// You can configure the DB URL for each database using a custom environment variable
// For example: ENV_KEY1=DB_URL1,ENV_KEY2=DB_URL2,...
const HASURA_GRAPHQL_DATABASE_URL = "HASURA_GRAPHQL_DATABASE_URL"

// The database URL for the metadata database
// If this env is not set, the server will use the primary database url HASURA_GRAPHQL_DATABASE_URL
const HASURA_GRAPHQL_METADATA_DATABASE_URL = "HASURA_GRAPHQL_METADATA_DATABASE_URL"

// The jwt secret config for the Hasura GraphQL Engine
const HASURA_GRAPHQL_JWT_SECRET = "HASURA_GRAPHQL_JWT_SECRET"

// The redis URL used by GraphQL Engine Plus for query caching
// This env is used by Hasura Cloud only, not the CE version
const HASURA_GRAPHQL_REDIS_URL = "HASURA_GRAPHQL_REDIS_URL"

// The redis URL used by GraphQL Engine Plus for query caching (read only)
// Note: Hasura Cloud may not use this env
const HASURA_GRAPHQL_REDIS_READER_URL = "HASURA_GRAPHQL_REDIS_READER_URL"

// The redis cluster URL used by GraphQL Engine Plus for query caching
// Note: Hasura Cloud  may not use this env
const HASURA_GRAPHQL_REDIS_CLUSTER_URL = "HASURA_GRAPHQL_REDIS_CLUSTER_URL"

// The maximum allowed cache TTL for a query
// If the query has ttl value greater than this max value, the cache will use this max value
// Default to 3600 seconds if the env is not set (same as Hasura Cloud)
// Set this value to 0 will disable query caching
const HASURA_GRAPHQL_CACHE_MAX_ENTRY_TTL = "HASURA_GRAPHQL_CACHE_MAX_ENTRY_TTL"

func SetupViper() {
	// setup automatic environment variable binding
	viper.AutomaticEnv()
}
func InitConfig() {
	// set default value for viper config
	viper.SetDefault(ENGINE_PLUS_SERVER_HOST, "")
	viper.SetDefault(ENGINE_PLUS_SERVER_PORT, "8000")
	viper.SetDefault(ENGINE_PLUS_GRAPHQL_V1_PATH, "/public/graphql/v1")
	viper.SetDefault(ENGINE_PLUS_GRAPHQL_V1_READONLY_PATH, "/public/graphql/v1readonly")
	viper.SetDefault(ENGINE_PLUS_HEALTH_CHECK_PATH, "/public/graphql/health")
	viper.SetDefault(ENGINE_PLUS_META_PATH, "/public/meta")
	viper.SetDefault(HASURA_GRAPHQL_CORS_DOMAIN, "*")
	viper.SetDefault(HASURA_GRAPHQL_CACHE_MAX_ENTRY_TTL, 3600)
	viper.SetDefault(ENGINE_PLUS_GRAPHQL_PRIMARY_VS_REPLICA_WEIGHT, 50)
	viper.SetDefault(ENGINE_PLUS_ENABLE_OPEN_TELEMETRY, "")
	viper.SetDefault(DEBUG_MODE, false)
	viper.SetDefault(ENGINE_PLUS_GROUPCACHE_PORT, "8879")
	viper.SetDefault(ENGINE_PLUS_GROUPCACHE_CLUSTER_MODE, "")
	viper.SetDefault(ENGINE_PLUS_GROUPCACHE_WAIT_ETA, 500)
	viper.SetDefault(ENGINE_PLUS_GROUPCACHE_MAX_SIZE, 60000000)

	// additional fix
	engineMetaPath := viper.GetString(ENGINE_PLUS_META_PATH)
	if engineMetaPath[len(engineMetaPath)-1] == '/' {
		// check if metadataPath end with /
		// if yes, remove / from the end of metadataPath.
		viper.Set(ENGINE_PLUS_META_PATH, engineMetaPath[:len(engineMetaPath)-1])
	}

	// limit the max value to 3000, this allow the group wait to loop for ~500-600 times
	// each loop will sleep for 5 miliseconds before check the redis cache again
	if viper.GetUint64(ENGINE_PLUS_GROUPCACHE_WAIT_ETA) > 3000 {
		viper.Set(ENGINE_PLUS_GROUPCACHE_WAIT_ETA, 3000)
	}
}

func StripTrailingSlash(path string) string {
	if path[len(path)-1:] == "/" {
		return path[:len(path)-1]
	}
	return path
}
