# branchbox/proxy — the data plane (sidecar and edge modes).
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/branchbox-proxy ./cmd/proxy

FROM alpine:3.22
RUN apk add --no-cache iptables su-exec
COPY --from=build /out/branchbox-proxy /usr/local/bin/branchbox-proxy
COPY deploy/sidecar-entrypoint.sh /usr/local/bin/sidecar-entrypoint.sh
# Guard against Windows line endings breaking the shell script.
RUN sed -i 's/\r$//' /usr/local/bin/sidecar-entrypoint.sh && chmod +x /usr/local/bin/sidecar-entrypoint.sh
ENTRYPOINT ["/usr/local/bin/sidecar-entrypoint.sh"]
CMD ["sidecar"]
