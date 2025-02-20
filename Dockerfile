FROM golang:1.23.3-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . .

RUN go build .

FROM alpine:latest

COPY --from=builder /app/triangles /usr/local/bin/triangles

ENV SECRET_KEY="SECRET_KEY"
ENV UNSPLASH_CLIENT_ID="UNSPLASH_CLIENT_ID"

CMD ["triangles"]
