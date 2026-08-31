FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /pro-ant ./cmd/proxy

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /pro-ant /pro-ant
COPY pool.yaml.example /pool.yaml.example
EXPOSE 8080 9090
ENTRYPOINT ["/pro-ant"]
CMD ["--config","/pool.yaml","--listen",":8080","--metrics-addr",":9090"]
