#!/bin/bash

# start graphql-engine RW
graphql-engine serve --server-port 8881 &
# wait till graphql-engine RW is up using healthz endpoint
while [[ "$(curl -s -o /dev/null -w ''%{http_code}'' localhost:8881/healthz)" != "200" ]]; do sleep 1; done
hasura metadata apply --project ./schema/v1 || exit 1;
# show metadata inconsistency if there is any
hasura metadata inconsistency list --project ./schema/v1



