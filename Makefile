# Makefile need to indent with tab instead of space
# indent with spaces lead to this error: Makefile:5: *** missing separator.  Stop.
SHELL := /bin/bash
# include default shell env for Makefile
include .make.env
LOCAL_DOCKER_IMG_REPO := graphql-engine-plus
# export all variable to sub Makefile as well
export

install:  ## install project dependencies
	# allow vscode to use python env in devcontainer to suggest 
	pip install -r src/scripting/requirements.txt
	# install type hinting requirements for python
	pip install -r typing-requirements.txt
	# install hasura cli if not yet installed
	[ -f /usr/local/bin/hasura ] || curl -L https://github.com/hasura/graphql-engine/raw/stable/cli/get.sh | bash

lint:
	golangci-lint run -v src
	
up:  ## run the project in local
	make build.out ARCH=$(shell dpkg --print-architecture)
	docker compose up -d
logs-follow-graphql-engine:
	docker compose logs -f graphql-engine
PROJECT := ./example/graphql-engine/schema/v1
hasura-console:  ## run hasura console for schema v1 localy at port 9695 (require port 9693 as well)
	hasura console --project $(PROJECT) --envfile .hasura-cli.env --address 0.0.0.0 --log-level DEBUG --api-host http://localhost --no-browser
hasura-metadata-export:  ## export graphql metadata to yaml files in example
	hasura metadata export --project $(PROJECT) --envfile .hasura-cli.env
hasura-metadata-apply:  ## apply graphql metadata yaml files in example
	hasura metadata apply --project $(PROJECT) --envfile .hasura-cli.env
hasura-metadata-show-inconsistent:  ## show inconsistent metadata yaml files in example
	hasura metadata inconsistency list --project $(PROJECT) --envfile .hasura-cli.env
hasura-deploy:  ## run migrations and apply graphql metadata yaml files in example
	hasura deploy --project $(PROJECT) --envfile .hasura-cli.env
hasura-migrate-create-migration:
	hasura migrate create "CHANGE-ME" --project $(PROJECT) --envfile .hasura-cli.env
hasura-migrate-apply-down-1:
	hasura migrate apply --down 1 --project $(PROJECT) --envfile .hasura-cli.env

run-migrate-hasura:
	docker compose run graphql-engine /bin/migrate

run-graphql-benchmark:
	docker run --rm --net=host -v "$$PWD/example/benchmark":/app/tmp -it gelmium/graphql-bench query --config="tmp/config.query.yaml" --outfile="tmp/report.json"
run-graphql-benchmark-readonly:
	docker run --rm --net=host -v "$$PWD/example/benchmark":/app/tmp -it gelmium/graphql-bench query --config="tmp/config.readonly-query.yaml" --outfile="tmp/report-readonly.json"	
redis-del-all-data:
	docker compose exec redis bash -c "redis-cli --scan --pattern data:* | xargs redis-cli del"
dynamodb-show-all-item:
	aws dynamodb --endpoint-url http://localhost:18000 execute-statement --statement 'SELECT * FROM "public.customer"'

build: $(shell find src -type f)  ## compile and build project
	# run a test build
	cd ./src; go build -o ./build/server .
	mkdir -p build && touch build


ARCH := amd64
build.out: build  ## build project and output build artifact (docker image/lambda zip file)
	# Run Build artifact such as: docker container image
	make build.graphql-engine-plus
	touch build.out

build.push: build.out  ## build project and push build artifact (docker image/lambda zip file) to registry
	docker tag $(LOCAL_DOCKER_IMG_REPO):latest $(REMOTE_DOCKER_IMG_REPO):$(HASURA_VERSION)
	docker tag $(LOCAL_DOCKER_IMG_REPO):latest $(REMOTE_DOCKER_IMG_REPO):$(REMOTE_DOCKER_IMG_TAG)
	docker push $(REMOTE_DOCKER_IMG_REPO):$(HASURA_VERSION)
	docker push $(REMOTE_DOCKER_IMG_REPO):$(REMOTE_DOCKER_IMG_TAG)

clean:  ## clean up build artifacts
	rm -rf ./dist ./build
	rm -f .*.out *.out

build.graphql-engine-plus:
	cd ./src/;go mod tidy
	docker build --build-arg="HASURA_GRAPHQL_ENGINE_VERSION=$(HASURA_VERSION)" --target=server --progress=plain --output=type=docker --platform linux/$(ARCH) -t $(LOCAL_DOCKER_IMG_REPO):latest ./src
go.run.proxy-benchmark:
	export GOMAXPROCS=8; cd ./example/benchmark/proxy/;go run .
python.run.scripting-server:
	# load .env file and run python server
	@set -o allexport; source .env; set +o allexport;cd ./src/scripting/;python3 server.py
F := test-script.py
P := test
upload-script:
	@set -o allexport; source .env; set +o allexport;curl -X POST $$SCRIPTING_UPLOAD_PATH -F "file=@$(F)" -F "path=$(P)" -H "X-Engine-Plus-Execute-Secret: $$ENGINE_PLUS_EXECUTE_SECRET"
upload-lib:
	@set -o allexport; source .env; set +o allexport;curl -X POST $$SCRIPTING_UPLOAD_PATH -F "file=@$(F)" -F "path=$(P)" -F "is_library=true" -H "X-Engine-Plus-Execute-Secret: $$ENGINE_PLUS_EXECUTE_SECRET"
deploy-script:
	@set -o allexport; source .env; set +o allexport;bash scripts/deploy_scripts.sh

PORT := 8000
test.graphql-engine-plus.query:
	# fire a curl request to graphql-engine-plus
	@curl -X POST -H "Content-Type: application/json" -H "X-Hasura-Admin-Secret: gelsemium" -d '{"query":"query MyQuery {customer(offset: 0, limit: 1) {id}}"}' http://localhost:$(PORT)/public/graphql/v1
	@curl -X POST -H "Content-Type: application/json" -H "X-Hasura-Admin-Secret: gelsemium" -d '{"query":"query MyQuery {customer(offset: 1, limit: 5) {id}}"}' http://localhost:$(PORT)/public/graphql/v1readonly
	@curl -X POST -H "Content-Type: application/json" -H "X-Hasura-Admin-Secret: gelsemium" -d '{"query":"query MyQuery @cached(ttl: 60){customer(offset: 1, limit: 50) {id}}"}' http://localhost:$(PORT)/public/graphql/v1
	@curl -X POST -H "Content-Type: application/json" -H "X-Hasura-Admin-Secret: gelsemium" -d '{"query":"query MyQuery @cached(ttl: 60){customer(offset: 1, limit: 50) {id}}"}' http://localhost:$(PORT)/public/graphql/v1readonly
test.graphql-engine-plus.query-cache:
	@curl -X POST -H "Content-Type: application/json" -H "Authorization: Bearer eyJ0eXAiOiJKV1QiLCJhbGciOiJIUzI1NiJ9.eyJpc3MiOiJPbmxpbmUgSldUIEJ1aWxkZXIiLCJpYXQiOjE3MTUyNDIyMDAsImV4cCI6MTc0Njc3ODIwMCwiYXVkIjoid3d3LmV4YW1wbGUuY29tIiwic3ViIjoianJvY2tldEBleGFtcGxlLmNvbSIsImFjY291bnRfaWQiOiIxIiwidG9rZW5fdHlwZSI6InVzZXIifQ.4MDc5lDRW-G05UP9VS2SJoskqG6FIb12u1pVj6jUUts" -d '{"query":"query MyQuery @cached(ttl: 60){customer(offset: 1, limit: 5) {id}}"}' http://localhost:$(PORT)/public/graphql/v1
	@curl -X POST -H "Content-Type: application/json" -H "Authorization: Bearer eyJ0eXAiOiJKV1QiLCJhbGciOiJIUzI1NiJ9.eyJpc3MiOiJPbmxpbmUgSldUIEJ1aWxkZXIiLCJpYXQiOjE3MTUyNDIyMDAsImV4cCI6MTc0Njc3ODIwMCwiYXVkIjoid3d3LmV4YW1wbGUuY29tIiwic3ViIjoianJvY2tldEBleGFtcGxlLmNvbSIsImFjY291bnRfaWQiOiIxIiwidG9rZW5fdHlwZSI6InVzZXIifQ.4MDc5lDRW-G05UP9VS2SJoskqG6FIb12u1pVj6jUUts" -d '{"query":"query MyQuery @cached(ttl: 60){customer(offset: 1, limit: 5) {id}}"}' http://localhost:$(PORT)/public/graphql/v1readonly
test.graphql-engine-plus.query-gzip: 
	# if there is few data, content will not be gzip
	@curl -X POST -H "Accept-Encoding: gzip" -H "Content-Type: application/json" -H "X-Hasura-Admin-Secret: gelsemium" -d '{"query":"query MyQuery {customer(offset: 1, limit: 255) {id}}"}' http://localhost:$(PORT)/public/graphql/v1 --output /tmp/result-query.json.gz
	@gunzip -c /tmp/result-query.json.gz
test.graphql-engine-plus.query-gzip-cache:
	@curl -X POST -H "Accept-Encoding: gzip" -H "Content-Type: application/json" -H "X-Hasura-Admin-Secret: gelsemium" -d '{"query":"query MyQuery @cached(ttl: 60){customer(offset: 1, limit: 255) {id}}"}' http://localhost:$(PORT)/public/graphql/v1 --output /tmp/result-query.json.gz
	@gunzip -c /tmp/result-query.json.gz
PORT1 := 8001
PORT2 := 8002
test.graphql-engine-plus.query-concurrent:
	# fire a curl request to graphql-engine-plus
	@curl -X POST -H "Content-Type: application/json" -H "X-Hasura-Admin-Secret: gelsemium" -d '{"query":"query MyQuery @cached(ttl: 60){customer(offset: 1, limit: 5) {id}}"}' http://localhost:$(PORT2)/public/graphql/v1 &
	@curl -X POST -H "Content-Type: application/json" -H "X-Hasura-Admin-Secret: gelsemium" -d '{"query":"query MyQuery @cached(ttl: 60){customer(offset: 1, limit: 5) {id}}"}' http://localhost:$(PORT1)/public/graphql/v1 &
	@curl -X POST -H "Content-Type: application/json" -H "X-Hasura-Admin-Secret: gelsemium" -d '{"query":"query MyQuery @cached(ttl: 60){customer(offset: 1, limit: 5) {id}}"}' http://localhost:$(PORT)/public/graphql/v1
	@curl -X POST -H "Content-Type: application/json" -H "X-Hasura-Admin-Secret: gelsemium" -d '{"query":"query MyQuery @cached(ttl: 60){customer(offset: 1, limit: 5) {id}}"}' http://localhost:$(PORT2)/public/graphql/v1readonly &
	@curl -X POST -H "Content-Type: application/json" -H "X-Hasura-Admin-Secret: gelsemium" -d '{"query":"query MyQuery @cached(ttl: 60){customer(offset: 1, limit: 5) {id}}"}' http://localhost:$(PORT1)/public/graphql/v1readonly &
	@curl -X POST -H "Content-Type: application/json" -H "X-Hasura-Admin-Secret: gelsemium" -d '{"query":"query MyQuery @cached(ttl: 60){customer(offset: 1, limit: 5) {id}}"}' http://localhost:$(PORT)/public/graphql/v1readonly
	
test.graphql-engine-plus.mutation:
	# fire a curl request to graphql-engine-plus
	@curl -X POST -H "Content-Type: application/json" -H "X-Hasura-Admin-Secret: gelsemium" -H "X-Hasura-Role: user" -d '{"query":"mutation CustomerMutation { insert_customer_one(object: {first_name: \"test\", external_ref_list: [\"text_external_ref\"], last_name: \"cus\"}) { id } }"}' http://localhost:$(PORT)/public/graphql/v1
	@curl -X POST -H "Content-Type: application/json" -H "X-Hasura-Admin-Secret: gelsemium" -d '{"query":"mutation MyMutation { quickinsert_customer_one(object: {first_name: \"test\", external_ref_list: []}) { id first_name created_at } } "}' http://localhost:$(PORT)/public/graphql/v1

N := 100
test.graphql-engine-plus.mutation-many:
	# fire many curl request to graphql-engine-plus
	@seq 1 $(N) | xargs -I {} -P 10 curl -X POST -H "Content-Type: application/json" -H "X-Hasura-Admin-Secret: gelsemium" -d '{"query":"mutation CustomerMutation { insert_customer_one(object: {first_name: \"test\", external_ref_list: [\"text_external_ref\"], last_name: \"cus\"}) { id } }"}' http://localhost:$(PORT)/public/graphql/v1