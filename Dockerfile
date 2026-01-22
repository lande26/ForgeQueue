# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build binaries
RUN go build -o /app/bin/producer ./cmd/producer
RUN go build -o /app/bin/worker ./cmd/worker
RUN go build -o /app/bin/reaper ./cmd/reaper

# Final stage
FROM alpine:latest

WORKDIR /app

COPY --from=builder /app/bin/producer /app/producer
COPY --from=builder /app/bin/worker /app/worker
COPY --from=builder /app/bin/reaper /app/reaper

# Default entrypoint (can be overridden)
CMD ["/app/producer"]
