# syntax=docker/dockerfile:1.7

FROM golang:1.25-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN go build -ldflags "-s -w" -o /out/calm ./cmd/calm \
 && go build -ldflags "-s -w" -o /out/calm-adapter ./cmd/calm-adapter

FROM gcr.io/distroless/static:nonroot AS calm
COPY --from=build /out/calm /usr/local/bin/calm
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/calm"]

FROM gcr.io/distroless/static:nonroot AS calm-adapter
COPY --from=build /out/calm-adapter /usr/local/bin/calm-adapter
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/calm-adapter"]
