FROM golang:1.24-alpine AS build

WORKDIR /src

COPY . .

RUN go mod tidy
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/3xui-user-sync ./cmd/main.go

FROM scratch

WORKDIR /app

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=65532:65532 /out/3xui-user-sync /app/3xui-user-sync
COPY --from=build --chown=65532:65532 /src/data /app/data

USER 65532:65532

ENV HTTP_ADDR=:8080
ENV DB_PATH=/app/data/app.db
ENV LOG_LEVEL=info
ENV LOG_FORMAT=pretty
ENV PUBLIC_SUBSCRIPTION_PATH=/sub/
ENV PROFILE_TITLE=3xui-user-sync
ENV SECURE_COOKIE=false
ENV REQUEST_TIMEOUT=15s
ENV SESSION_TTL=24h
ENV SESSION_IDLE_TIMEOUT=8h
ENV REMEMBER_ME_TTL=720h

EXPOSE 8080

ENTRYPOINT ["/app/3xui-user-sync"]
