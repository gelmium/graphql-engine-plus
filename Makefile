# Makefile need to indent with tab instead of space
# indent with spaces lead to this error: Makefile:5: *** missing separator.  Stop.
SHELL := /bin/bash
# include default shell env for Makefile
include .make.env
# export all variable to sub Makefile as well
export

install:  ## install project dependencies
	# allow vscode to use python env in devcontainer to suggest 
	pip install -r src/scripting/requirements.txt
	# install hasura cli if not yet installed
	[ -f /usr/local/bin/hasura ] || curl -L https://github.com/hasura/graphql-engine/raw/stable/cli/get.sh | bash

golangci-lint:
	docker run -it --rm -v "$$PWD/src":/src -w /src golangci/golangci-lint:latest golangci-lint run -v	
	
up:  ## run the project in local
	make build.out ARCH=$(shell dpkg --print-architecture)
	docker-compose up -d
	bash scripts/prepare_local_redis.sh
logs-follow-graphql-engine:
	docker-compose logs -f graphql-engine

hasura-metadata-export-example-v1:  ## export graphql metadata to yaml files in src/schema/v1
	hasura metadata export --project ./example/graphql-engine/schema/v1
hasura-metadata-apply-example-v1:  ## apply graphql metadata yaml files in src/schema/v1
	hasura metadata apply --project ./example/graphql-engine/schema/v1
hasura-metadata-show-inconsistent-example-v1:  ## show inconsistent metadata yaml files in src/schema/v1
	hasura metadata inconsistency list --project ./example/graphql-engine/schema/v1

hasura-migratev1-create-migration-from-server:
	docker-compose exec graphql-engine hasura migrate create "CHANGE-ME" --from-server --database-name default --project ./schema/v1 --schema public
hasura-deploy-v1:
	docker-compose exec graphql-engine hasura deploy --project ./schema/v1

run-migrate-hasura:
	docker-compose run graphql-engine /root/migrate_hasura.sh
N := 1000
run-warmup-apprunner:
	docker run --rm -it haydenjeune/wrk2:latest -t4 -c$(N) -d100s -R$(shell expr 10 \* $(N) + 100 ) --latency $(WARMUP_HEALTH_ENDPOINT_URL)?sleep=100000
run-graphql-benchmark:
	docker run --rm --net=host -v "$$PWD/example/benchmark":/app/tmp -it gelmium/graphql-bench query --config="tmp/config.query.yaml" --outfile="tmp/report.json"
run-graphql-benchmark-readonly:
	docker run --rm --net=host -v "$$PWD/example/benchmark":/app/tmp -it gelmium/graphql-bench query --config="tmp/config.readonly-query.yaml" --outfile="tmp/report-readonly.json"	
run-redis-insight:
	docker run --rm --name redisinsight -v redisinsight:/db -p 5001:8001 redislabs/redisinsight:latest
redis-del-all-data:
	docker-compose exec redis bash -c "redis-cli --scan --pattern data:* | xargs redis-cli del"
	
build: $(shell find src -type f)  ## compile and build project
	mkdir -p build && touch build

LOCAL_DOCKER_IMG_REPO := graphql-engine-plus
ARCH := amd64
build.out: build  ## build project and output build artifact (docker image/lambda zip file)
	# Run Build artifact such as: docker container image
	make build.graphql-engine-plus
	make build.graphql-engine-plus-nginx
	touch build.out

build.push: build.out  ## build project and push build artifact (docker image/lambda zip file) to registry
	docker push $(LOCAL_DOCKER_IMG_REPO):latest

clean:  ## clean up build artifacts
	rm -rf ./dist ./build
	rm -f .*.out *.out

HASURA_VERSION := v2.33.4
build.graphql-engine-plus:
	cd ./src/;go mod tidy
	docker build --build-arg="HASURA_GRAPHQL_ENGINE_VERSION=$(HASURA_VERSION)" --target=server --progress=plain --output=type=docker --platform linux/$(ARCH) -t $(LOCAL_DOCKER_IMG_REPO):latest ./src
build.graphql-engine-plus-nginx:
	docker build --output=type=docker --platform linux/$(ARCH) -t $(LOCAL_DOCKER_IMG_REPO):nginx-latest -f nginx.Dockerfile ./src
build.example-runner:
	cd ./example/backend/runner/;go mod tidy
	docker build --target=server --progress=plain --output=type=docker --platform linux/$(ARCH) -t apprunner-example:latest ./example/backend/runner
go.run.example-runner:
	cd ./example/backend/runner/;go run .
go.run.proxy-benchmark:
	export GOMAXPROCS=8; cd ./example/benchmark/proxy/;go run .
python.run.scripting-server:
	cd ./src/scripting/;python3 server.py
test.graphql-engine-plus.query:
	# fire a curl request to graphql-engine-plus
	@curl -X POST -H "Content-Type: application/json" -H "X-Hasura-Admin-Secret: gelsemium" -d '{"query":"query MyQuery {customer(offset: 0, limit: 1) {id}}"}' http://localhost:8000/public/graphql/v1
	@curl -X POST -H "Content-Type: application/json" -H "X-Hasura-Admin-Secret: gelsemium" -d '{"query":"query MyQuery {customer(offset: 0, limit: 1) {id}}"}' http://localhost:8000/public/graphql/v1readonly
test.graphql-engine-plus.mutation:
	# fire a curl request to graphql-engine-plus
	@curl -X POST -H "Content-Type: application/json" -H "X-Hasura-Admin-Secret: gelsemium" -H "X-Hasura-Role: user" -d '{"query":"mutation CustomerMutation { insert_customer_one(object: {first_name: \"test\", external_ref_list: [\"text_external_ref\"], last_name: \"cus\"}) { id } }"}' http://localhost:8000/public/graphql/v1
	@curl -X POST -H "Content-Type: application/json" -H "X-Hasura-Admin-Secret: gelsemium" -d '{"query":"mutation MyMutation { quickinsert_customer_one(object: {first_name: \"test\", external_ref_list: []}) { id first_name created_at } } "}' http://localhost:8000/public/graphql/v1

test.example.backend.lambda:
	python3 -m unittest discover -s ./example/backend/lambda -p "*_test.py"