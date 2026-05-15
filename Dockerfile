FROM golang:1.26 AS build

WORKDIR /src
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/mfp ./cmd/mfp \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/mock-provider ./cmd/mock-provider

FROM gcr.io/distroless/static-debian12:nonroot AS runtime

WORKDIR /app
COPY --from=build /out/mfp /app/mfp
COPY --from=build /out/mock-provider /app/mock-provider
COPY --chown=nonroot:nonroot configs /app/configs

USER nonroot:nonroot
EXPOSE 18320 18321 4000
ENTRYPOINT ["/app/mfp"]
