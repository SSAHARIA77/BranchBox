# branchbox/controller — the control plane. It needs git (to fetch branches)
# and the docker CLI with buildx (to build and run sandboxes through the
# mounted Docker socket).
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/branchbox-controller ./cmd/controller

FROM alpine:3.22
RUN apk add --no-cache git docker-cli docker-cli-buildx ca-certificates \
    && git config --system --add safe.directory '*'
COPY --from=build /out/branchbox-controller /usr/local/bin/branchbox-controller
ENTRYPOINT ["/usr/local/bin/branchbox-controller"]
