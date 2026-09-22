# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build

WORKDIR /src

# Cache dependency downloads separately from application source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/unoparty .

FROM scratch

COPY --from=build /out/unoparty /unoparty

EXPOSE 8080
ENV PORT=8080
USER 65532:65532

ENTRYPOINT ["/unoparty"]
