package main

import (
	"os"
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
var roPath = os.Getenv("ENGINE_PLUS_GRAPHQL_V1_READONLY_PATH")

// The API PATH for the scripting engine
// If not set, no public endpoint will be exposed to send request to scripting engine
// scripting engine can still be used in hasura metadata by using the local endpoint
// at http://localhost:8880/execute
// or at http://localhost:8880/validate
var scriptingPublicPath = os.Getenv("ENGINE_PLUS_SCRIPTING_PUBLIC_PATH")

// The secret key to authenticate the request to the scripting engine
// Must be set if the execPath is set
var envExecuteSecret = os.Getenv("ENGINE_PLUS_EXECUTE_SECRET")

// The API PATH for the health check endpoint. This endpoint will do health checks
// of all dependent services, ex: Hasura graphql-engine, Python scripting-engine, etc.
// default to /public/graphql/health if the env is not set
var healthCheckPath = os.Getenv("ENGINE_PLUS_HEALTH_CHECK_PATH")

// The API PATH to enable schema migrations, console, etc.
// default to /public/meta/ if the env is not set
var engineMetaPath = os.Getenv("ENGINE_PLUS_META_PATH")

// The weight to round-robin between primary and replica
// The value must be between 0 and 100. 100 mean all queries will go to primary
// 0 mean all queries will go to replica. The default value is 50.
// This value is only used if the HASURA_GRAPHQL_READ_REPLICA_URLS is set
var engineGqlPvRweight = os.Getenv("ENGINE_PLUS_GRAPHQL_PRIMARY_VS_REPLICA_WEIGHT")

// Set to "http" or "grpc" to enable Open Telemetry tracing
// "http" will use the HTTP exporter which sent trace to https://localhost:4318/v1/traces.
// to customise please see this: https://pkg.go.dev/go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp
// "grpc" will use the gRPC exporter which sent trace to https://localhost:4317.
// to customise please see this: https://pkg.go.dev/go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc
// default to empty string if the env is not set. (Disable Open Telemetry)
var engineEnableOtelType = os.Getenv("ENGINE_PLUS_ENABLE_OPEN_TELEMETRY")

// Set to "true" to enable debug mode
var _d = os.Getenv("DEBUG")
var debugMode = _d == "true" || _d == "True" || _d == "yes" || _d == "Yes" || _d == "1"

// Below are the environment variables that are also used by Hasura GraphQL Engine

// The CORS domain to allow requests from. By default we enable CORS for all domain.
// default to "*" if the env is not set
var hasuraGqlCorsDomain = os.Getenv("HASURA_GRAPHQL_CORS_DOMAIN")

// The database URL for the replica database
// If this env is set, the server will start another Hasura GraphQL Engine server
// which configure to the read-replica database. The engine plus will check if the
// graphql query is a read-only query, it will route the query to the both
// primary & replica server using the round-robin with the weight defined in enviroment
// if the query is a write query, it will only route to the primary server
// default to empty string if the env is not set
var hasuraGqlReadReplicaUrls = os.Getenv("HASURA_GRAPHQL_READ_REPLICA_URLS")
