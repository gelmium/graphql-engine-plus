#!/bin/bash

# start graphql-engine RW and save the pid
graphql-engine serve --server-port 8881 &
pid=$!
# wait till graphql-engine RW is up using healthz endpoint
while [[ "$(curl -s -o /dev/null -w ''%{http_code}'' localhost:8881/healthz)" != "200" ]]; do sleep 1; done
hasura metadata deploy --project /graphql-engine/schema || exit 1;
# show metadata inconsistency if there is any
hasura metadata inconsistency list --project /graphql-engine/schema
# send SIGTERM to graphql-engine RW
kill -15 $pid
# wait till graphql-engine RW is down, by checking if the port is still open
while [[ "$(lsof -i :8881)" ]]; do sleep 1; done


