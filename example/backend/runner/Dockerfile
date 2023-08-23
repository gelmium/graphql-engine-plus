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
RUN apk update && apk add -U --no-cache ca-certificates && update-ca-certificates

FROM scratch AS server
# Copy ssl CA certificates
COPY --from=build-server /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
# Copy and set the ENTRYPOINT to the executable
COPY --from=build-server /bin/server /bin/
ENTRYPOINT ["/bin/server"]
# Expose port 3000
EXPOSE 3000
# Add healthcheck
HEALTHCHECK --interval=30s --timeout=3s CMD curl --fail http://localhost:3000/health || exit 1