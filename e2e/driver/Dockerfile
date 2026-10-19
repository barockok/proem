FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
COPY e2e/driver ./e2e/driver
RUN CGO_ENABLED=0 go test -c -tags e2e -o /driver.test ./e2e/driver

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /driver.test /driver.test
ENTRYPOINT ["/driver.test", "-test.v", "-test.timeout=10m"]
