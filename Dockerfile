FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /proem ./cmd/proem

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /proem /proem
COPY pool.yaml.example /pool.yaml.example
COPY clients.yaml.example /clients.yaml.example
EXPOSE 8080 9090
ENTRYPOINT ["/proem"]
CMD ["--config","/pool.yaml","--clients","/clients.yaml","--listen",":8080","--metrics-addr",":9090"]
