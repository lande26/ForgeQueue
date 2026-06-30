# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -o /app/bin/api ./cmd/api
RUN go build -o /app/bin/worker ./cmd/worker
RUN go build -o /app/bin/reaper ./cmd/reaper

# Final stage
FROM alpine:latest

WORKDIR /app

COPY --from=builder /app/bin/api /app/api
COPY --from=builder /app/bin/worker /app/worker
COPY --from=builder /app/bin/reaper /app/reaper

CMD ["/app/api"]
