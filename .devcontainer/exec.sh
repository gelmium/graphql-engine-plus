#!/bin/bash
SCRIPT_DIR=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )
docker exec -it $(docker ps -f "label=`cat $SCRIPT_DIR/.env`" --format "{{.ID}}") sh -c "cd /workspaces/graphql-engine-plus;bash"