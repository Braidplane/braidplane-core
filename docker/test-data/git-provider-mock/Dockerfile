FROM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS build

WORKDIR /app
COPY go.mod .
COPY main.go .
RUN go build -o git-provider-mock .

FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk add --no-cache ca-certificates wget
WORKDIR /app
COPY --from=build /app/git-provider-mock /app/git-provider-mock
EXPOSE 8080
CMD ["/app/git-provider-mock"]
