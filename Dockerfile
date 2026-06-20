FROM golang:1.24-alpine AS build

WORKDIR /app
COPY go.mod ./
COPY . .
RUN go test ./... && go build -o /out/yi-flow-knowledge-base ./cmd/server

FROM alpine:3.21
WORKDIR /app
RUN addgroup -S app && adduser -S app -G app
COPY --from=build /out/yi-flow-knowledge-base /app/yi-flow-knowledge-base
RUN mkdir -p /var/lib/yi-flow-knowledge-base && chown -R app:app /var/lib/yi-flow-knowledge-base /app
USER app
EXPOSE 8080
ENV ADDR=:8080
ENV STORAGE_DIR=/var/lib/yi-flow-knowledge-base
CMD ["/app/yi-flow-knowledge-base"]
