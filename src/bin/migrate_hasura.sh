#!/bin/bash

# start graphql-engine RW and save the pid
graphql-engine serve --server-port 8881 &
pid=$!
# wait till graphql-engine RW is up using healthz endpoint
while [[ "$(curl -s -o /dev/null -w ''%{http_code}'' localhost:8881/healthz)" != "200" ]]; do sleep 1; done
echo "Deploying migrations and metadata"
hasura deploy --project /graphql-engine/schema --endpoint "http://localhost:8881"
# save the exit code of the previous command
exit_code=$?
# if the exit code is 0, check for migrations status
if [[ $exit_code -eq 0 ]]; then
  # show the status of migrations
  echo | hasura migrate status --project /graphql-engine/schema --endpoint "http://localhost:8881"
  # override with the exit code of the previous command
  exit_code=$?
fi
# if the exit code is 0, check for metadata inconsistency
if [[ $exit_code -eq 0 ]]; then
  # show metadata inconsistency if there is any
  hasura metadata inconsistency list --project /graphql-engine/schema --endpoint "http://localhost:8881"
  # check if there is any metadata inconsistency
  if [[ $? -ne 0 ]]; then
    # print in red color
    echo -e "\033[0;31mMetadata inconsistency found\033[0m"
  fi
fi
# send SIGTERM to stop graphql-engine RW
kill -15 $pid
# wait till graphql-engine RW is down, by checking if the port is still open
while [[ "$(curl -s -o /dev/null -w ''%{http_code}'' localhost:8881/healthz)" == "200" ]]; do sleep 1; done
if [[ $exit_code -eq 0 ]]; then
  # print in green color
  echo -e "\033[0;32mMigrations and metadata deployed successfully\033[0m"
fi
# exit with the exit code of executed commands
exit $exit_code
