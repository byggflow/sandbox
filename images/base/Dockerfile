FROM alpine:3.20
RUN apk add --no-cache ca-certificates curl git bash coreutils
ARG TARGETOS=linux
ARG TARGETARCH=amd64
COPY bin/${TARGETOS}/${TARGETARCH}/sandbox-agent /usr/local/bin/sandbox-agent
WORKDIR /root
ENTRYPOINT ["/usr/local/bin/sandbox-agent"]
