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
	
up:  ## run the project in local
	make build.out ARCH=$(shell dpkg --print-architecture)
	docker-compose up -d
	bash scripts/prepare_local_redis.sh

hasura-metadatav1-export:  ## export graphql metadata to yaml files in src/schema/v1
	docker-compose exec graphql-engine hasura metadata export --project ./schema/v1
hasura-metadatav1-apply:  ## apply graphql metadata yaml files in src/schema/v1
	docker-compose exec graphql-engine hasura metadata apply --project ./schema/v1
hasura-metadatav1-show-inconsistent:  ## show inconsistent metadata yaml files in src/schema/v1
	docker-compose exec graphql-engine hasura metadata inconsistency list --project ./schema/v1

hasura-migratev1-create-migration-from-server:
	docker-compose exec graphql-engine hasura migrate create "CHANGE-ME" --from-server --database-name default --project ./schema/v1 --schema public
hasura-deploy-v1:
	docker-compose exec graphql-engine hasura deploy --project ./schema/v1

run-migrate-hasura:
	docker-compose run graphql-engine /root/migrate_hasura.sh
run-graphql-benchmark:
	docker run --rm --name graphql-bench --net=host -v "$$PWD/example/benchmark":/app/tmp -it gelmium/graphql-bench query --config="tmp/config.query.yaml" --outfile="tmp/report.json"
run-redis-insight:
	docker run --rm --name redisinsight -v redisinsight:/db -p 5001:8001 redislabs/redisinsight:latest
run-hasura-console:
	docker run --rm --name hasuraconsole -env-file ./env -p 8080:8000 hasura/graphql-engine:latest
redis-del-all-data:
	docker-compose exec redis bash -c "redis-cli --scan --pattern data:* | xargs redis-cli del"
	
build: $(shell find src -type f)  ## compile and build project
	mkdir -p build && touch build

LOCAL_DOCKER_IMG_REPO := gelmium/graphql-engine-plus
ARCH := amd64
build.out: build  ## build project and output build artifact (docker image/lambda zip file)
	# Run Build artifact such as: docker container image
	docker build --output=type=docker --platform linux/$(ARCH) -t $(LOCAL_DOCKER_IMG_REPO):latest ./src
	touch build.out

build.push: build.out  ## build project and push build artifact (docker image/lambda zip file) to registry
	docker push $(LOCAL_DOCKER_IMG_REPO):latest

clean:  ## clean up build artifacts
	rm -rf ./dist ./build
	rm -f .*.out *.out

build.graphql-engine-plus:
	docker build --target=server --progress=plain --output=type=docker --platform linux/$(ARCH) -t graphql-engine-plus:latest ./src

build.runner:
	docker build --target=server --progress=plain --output=type=docker --platform linux/$(ARCH) -t apprunner-example:latest ./example/backend/runner
go.run.runner:
	cd ./example/backend/runner/;go run .	

test.graphql-engine-plus:
	# fire a curl request to graphql-engine-plus
	curl -X POST -H "Content-Type: application/json" -H "X-Hasura-Admin-Secret: gelsemium" -d '{"query":"mutation CustomerMutation { insert_customer_one(object: {first_name: \"test\", external_ref_list: [\"text_external_ref\"], last_name: \"cus\"}) { id } }"}' http://localhost:8000/public/graphql/v1