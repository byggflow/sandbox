FROM byggflow/sandbox-base:latest AS base

FROM node:22-alpine
RUN apk add --no-cache ca-certificates curl git bash coreutils
COPY --from=base /usr/local/bin/sandbox-agent /usr/local/bin/sandbox-agent
WORKDIR /root
ENTRYPOINT ["/usr/local/bin/sandbox-agent"]
