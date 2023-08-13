#!/bin/bash
echo "Enable keyspace notifications"
# https://redis.io/docs/manual/keyspace-notifications/
docker-compose exec redis bash -c "redis-cli config set notify-keyspace-events Egdxe"