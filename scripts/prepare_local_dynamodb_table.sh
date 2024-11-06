#!/bin/bash
ENDPOINT_FLAG="--endpoint-url http://localhost:18000"
# wait for dynamodb on port 18000 to be ready, if wait for more than 60s, then exit
count=0
until aws dynamodb list-tables $ENDPOINT_FLAG || false; do
  count=$((count+1))
  if [ $count -gt 30 ]; then
    echo "DynamoDB is not ready after 60 seconds. Exiting..."
    exit 1
  fi
  echo "DynamoDB is not ready yet. Retrying $count..."
  sleep 2
done

echo "Create tables: $DYNAMODB_TABLE_NAME"
envsubst '$DYNAMODB_TABLE_NAME' < ./scripts/create-dynamodb-table.template.json > /tmp/create-dynamodb-table.json
aws dynamodb create-table $ENDPOINT_FLAG --no-cli-auto-prompt --cli-input-json file:///tmp/create-dynamodb-table.json || true

echo "Show all created tables"
aws dynamodb list-tables $ENDPOINT_FLAG 
