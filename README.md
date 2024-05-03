# graphql-engine-plus
A Graphql Engine with more features based on Hasura Graphql Engine CE

# Benchmark
You can use this web app to open the json file output of the benchmark tool:

https://hasura.github.io/graphql-bench/app/web-app/

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
