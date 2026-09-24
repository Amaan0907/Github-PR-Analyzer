# ---- build stage ----
FROM golang:1.25-alpine AS builder

WORKDIR /src

# copy just the module files first so `go mod download` is cached
# and doesn't rerun on every source change, only on dependency changes
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /out/api ./cmd/api

# ---- run stage ----
FROM alpine:3.20

RUN apk add --no-cache ca-certificates

WORKDIR /app
COPY --from=builder /out/api .

EXPOSE 3000

ENTRYPOINT ["./api"]
