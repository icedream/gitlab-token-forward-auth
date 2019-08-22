# To use this Docker image, insert a config file at /data/config.{toml|yaml|yml|json)
# or use AUTHD_* environment variables to configure without a file.

FROM golang:1.12-alpine
WORKDIR /usr/src/gitlab-token-forward-auth
COPY . .
ENV GOBIN /target/bin/
RUN mkdir -p "${GOBIN}"
RUN apk add --no-cache git
RUN go install -v ./cmd/...

###

FROM alpine:latest
RUN apk add --no-cache ca-certificates
COPY --from=0 /target/ /usr/local/
WORKDIR /data
ENTRYPOINT ["gitlab-token-authd"]
