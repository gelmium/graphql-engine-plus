#!/bin/bash
echo "Enable keyspace notifications"
# https://redis.io/docs/manual/keyspace-notifications/
docker compose exec redis bash -c "redis-cli -u redis://redisuser:redispwd@127.0.0.1:6379 config set notify-keyspace-events Egdxe"