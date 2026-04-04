FROM byggflow/sandbox-base:latest AS base

FROM python:3.13-alpine
RUN apk add --no-cache ca-certificates curl git bash coreutils
COPY --from=base /usr/local/bin/sandbox-agent /usr/local/bin/sandbox-agent
RUN adduser -D -h /home/sandbox sandbox
USER sandbox
WORKDIR /home/sandbox
ENTRYPOINT ["/usr/local/bin/sandbox-agent"]
