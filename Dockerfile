FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
# -trimpath/-buildvcs=false keep local paths and VCS data out of the binary.
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w" -o /tempmail ./cmd/tempmail

FROM scratch
COPY --from=build /tempmail /tempmail
USER 65534:65534
EXPOSE 2525 8080
ENTRYPOINT ["/tempmail"]
