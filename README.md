# graphql-engine-plus
A Graphql Engine with more features based on Hasura Graphql Engine CE

# Benchmark
You can use this web app to open the json file output of the benchmark tool:

https://hasura.github.io/graphql-bench/app/web-app/

## Memory consumation
Due to the addtional processes run, the GraphQL Engine Plus consumes more memory than the original Hasura GraphQL Engine CE. Below is some metrics.

### Without HASURA_GRAPHQL_REDIS_URL is configured (no redis cache):
##### with only HASURA_GRAPHQL_DATABASE_URL is configured (primary), default settings:
Startup memory: 200Mib
Avg memory usage: 240Mib 

##### with HASURA_GRAPHQL_READ_REPLICA_URLS is configured (primary + replica), default settings:
Startup memory: 370Mib
Avg memory usage: 430Mib

### With HASURA_GRAPHQL_REDIS_URL is configured (redis cache enabled):
turn on caching help reduce average memory usage of the Hasura Graphql Engine
##### with only HASURA_GRAPHQL_DATABASE_URL is configured (primary), default settings:
Startup memory: 210Mib
Avg memory usage: 240Mib
When internal cache full: 300Mib

##### with HASURA_GRAPHQL_READ_REPLICA_URLS is configured (primary + replica), default settings:
Startup memory: 380Mib
Avg memory usage: 420Mib
When internal cache full: 480Mib

reduce `ENGINE_PLUS_GROUPCACHE_MAX_SIZE` (default 60Mib) can help limit the max memory usage.

# Usage
Use this image as the base image for your Dockerfile: gelmium/graphql-engine-plus:latest

Your pythons scripts should be placed in the following directory: /graphql-engine/scripts

Example:
```Dockerfile
FROM gelmium/graphql-engine-plus:latest
# copy python scripts to allow the engine to run them
COPY src/scripts /graphql-engine/scripts
# copy hasura metadata schema to allow the engine to auto migrate
COPY src/schema /graphql-engine/schema
# run the migrate cli script and then the server
CMD ["/bin/migrate", "&&", "/bin/server"]
```
optionaly you can use hasura cli to deploy the schema manually. Then you can end the Dockerfile with `CMD ["/bin/server"]`

## Runtime configuration
Please read content of `src/config.go` to see all environment variables that can be used to configure the graphql engine plus.