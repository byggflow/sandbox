FROM golang:1.25-alpine AS agent-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /sandbox-agent ./cmd/sandbox-agent

FROM alpine:3.20
RUN apk add --no-cache ca-certificates curl git bash coreutils
COPY --from=agent-build /sandbox-agent /usr/local/bin/sandbox-agent
RUN adduser -D -h /home/sandbox sandbox
USER sandbox
WORKDIR /home/sandbox
ENTRYPOINT ["/usr/local/bin/sandbox-agent"]
