FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bot ./cmd/bot

FROM alpine:3.22 AS runtime
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 lab
USER lab

FROM runtime AS bot
COPY --from=build /out/bot /usr/local/bin/bot
ENTRYPOINT ["/usr/local/bin/bot"]

# The default target remains the server for plain `docker build` invocations.
FROM runtime AS server
COPY --from=build /out/server /usr/local/bin/server
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/server"]
