FROM golang:1.22-bookworm AS builder

WORKDIR /app

# Install C toolchain for h3-go CGO
RUN apt-get update && apt-get install -y --no-install-recommends gcc libc6-dev && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=1 GOOS=linux go build -o broker ./cmd/broker

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*

COPY --from=builder /app/broker /usr/local/bin/broker

EXPOSE 8080

ENTRYPOINT ["broker"]
