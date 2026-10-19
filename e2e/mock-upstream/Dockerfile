FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY e2e/mock-upstream/main.go .
RUN CGO_ENABLED=0 go build -trimpath -o /mock-upstream main.go

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /mock-upstream /mock-upstream
EXPOSE 8443
ENTRYPOINT ["/mock-upstream"]
