ARG HASURA_GRAPHQL_ENGINE_VERSION
# Use the official Go image as the base image
FROM golang:alpine AS base
# Set the working directory inside the container
WORKDIR /src
# Mount the go mod files into the container and download the dependencies
RUN --mount=type=cache,target=/go/pkg/mod/ \
    --mount=type=bind,source=go.sum,target=go.sum \
    --mount=type=bind,source=go.mod,target=go.mod \
    go mod download -x


FROM base AS build-server
# Mount the source code into the container Build the Go application 
RUN --mount=type=cache,target=/go/pkg/mod/ \
    --mount=type=bind,target=. \
    go build -o /bin/server .

FROM hasura/graphql-engine:${HASURA_GRAPHQL_ENGINE_VERSION:-v2.45.2} AS server
WORKDIR /graphql-engine

# install python and pip
RUN apt update \
    && apt install -y python3 python3-pip \
    && apt-get clean \
    && rm -rf /tmp/* /var/tmp/* /var/cache/apt/* /var/lib/apt/lists/*
# install scripting server dependencies
ADD ./scripting/requirements.txt /graphql-engine/scripting/requirements.txt
RUN pip3 install -r /graphql-engine/scripting/requirements.txt \
    && rm -rf /tmp/* /var/tmp/* /root/.cache/pip/*
# install hasura cli
RUN curl -L https://github.com/hasura/graphql-engine/raw/stable/cli/get.sh | VERSION=${HASURA_GRAPHQL_ENGINE_VERSION:-v2.42.0} bash
# Add scripting-server and migrate scripts 
COPY ./scripting /graphql-engine/scripting
ADD ./bin/migrate_hasura.sh /bin/migrate
RUN chmod +x /bin/migrate

# Copy and set the CMD to the executable
COPY --from=build-server /bin/server /bin/
CMD ["/bin/server"]

# Add healthcheck
HEALTHCHECK --interval=30s --timeout=3s CMD curl --fail http://localhost:${ENGINE_PLUS_SERVER_PORT:-8000}/health || exit 1
