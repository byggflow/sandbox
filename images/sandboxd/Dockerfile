FROM alpine:3.20
RUN apk add --no-cache ca-certificates
ARG TARGETOS=linux
ARG TARGETARCH=amd64
COPY bin/${TARGETOS}/${TARGETARCH}/sandboxd /usr/local/bin/sandboxd
ENTRYPOINT ["/usr/local/bin/sandboxd"]
