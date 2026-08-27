FROM golang:1.21-alpine AS builder

WORKDIR /app

COPY go.mod ./
# If there were dependencies we would copy go.sum and run go mod download, but for now we just have go.mod
RUN go mod download || true

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o auth-service .

FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /root/

COPY --from=builder /app/auth-service .

ENV PORT=8081
EXPOSE 8081

CMD ["./auth-service"]