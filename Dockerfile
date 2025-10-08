# Build stage
FROM golang:1.25.2-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o main .

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /app
COPY --from=builder /app/main .
COPY --from=builder /app/models.json .
COPY --from=builder /app/fingerprints.json .
COPY --from=builder /app/dashboard.html .
EXPOSE 8080
CMD ["./main"]