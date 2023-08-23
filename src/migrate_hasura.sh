#!/bin/bash

# start graphql-engine v1
graphql-engine serve --server-port 8881 &
# wait till graphql-engine v1 is up using healthz endpoint
while [[ "$(curl -s -o /dev/null -w ''%{http_code}'' localhost:8881/healthz)" != "200" ]]; do sleep 1; done
hasura metadata apply --project ./schema/v1 || exit 1;
# show metadata inconsistency if there is any
hasura metadata inconsistency list --project ./schema/v1

# start graphql-engine schema v2, and run migrate
if [ -n "$HASURA_GRAPHQL_METADATA_DATABASE_URL_V2" ]; then
    graphql-engine serve --server-port 8882 --metadata-database-url "$HASURA_GRAPHQL_METADATA_DATABASE_URL_V2" &
    # wait till graphql-engine v2 is up using healthz endpoint
    while [[ "$(curl -s -o /dev/null -w ''%{http_code}'' localhost:8882/healthz)" != "200" ]]; do sleep 1; done
    hasura metadata apply --project ./schema/v1 || exit 1;
    # show metadata inconsistency if there is any
    hasura metadata inconsistency list --project ./schema/v1
fi



