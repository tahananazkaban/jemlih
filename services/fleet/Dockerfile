FROM golang:1.27.3-alpine3.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY pkg ./pkg
COPY services/fleet ./services/fleet
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/fleet ./services/fleet/cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/fleet /fleet
EXPOSE 8080
ENTRYPOINT ["/fleet"]
