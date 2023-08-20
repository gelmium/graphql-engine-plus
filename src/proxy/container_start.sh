#!/bin/bash

# start graphql-engine schema v1
graphql-engine serve --server-port 8881 &
export REPLICA_PORT=8881
# start graphql-engine schema v2, if the env is set
if [ -n "$HASURA_GRAPHQL_READ_REPLICA_URLS" ]; then
    graphql-engine serve --server-port 8880 --database-url "$HASURA_GRAPHQL_READ_REPLICA_URLS" &
    export REPLICA_PORT=8880
fi
if [ -n "$HASURA_GRAPHQL_METADATA_DATABASE_URL_V2" ]; then
    graphql-engine serve --server-port 8882 --metadata-database-url "$HASURA_GRAPHQL_METADATA_DATABASE_URL_V2" &
fi
# start nginx
envsubst '\$SERVER_HOST \$SERVER_PORT \&REPLICA_PORT' < /var/www/conf/nginx_docker.conf.template > /var/www/conf/nginx.conf
nginx -c /var/www/conf/nginx.conf &

# start scripting server at port 8888
cd /graphql-engine/scripting/
python3 server.py &

# Wait for any process to exit, does not matter if it is graphql-engine, python or nginx
wait -n

# if any process exits then this container Exit with status of process that exited first
exit $?