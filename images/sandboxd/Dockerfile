FROM alpine:3.20
RUN apk add --no-cache ca-certificates
ARG TARGETOS TARGETARCH
COPY bin/${TARGETOS}/${TARGETARCH}/sandboxd /usr/local/bin/sandboxd
ENTRYPOINT ["/usr/local/bin/sandboxd"]
