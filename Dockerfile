# The SQLite driver is pure Go, so the bot builds without cgo and runs on a
# scratch image with nothing else in it.
FROM golang:1.25-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/timetracker .

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
# The tz database is embedded in the binary, so timezone settings work here.
COPY --from=build /out/timetracker /app/timetracker
# Mount a volume here to keep the database across restarts.
VOLUME ["/app/data"]
ENV DATABASE_PATH=/app/data/timetracker.db
USER nonroot:nonroot
ENTRYPOINT ["/app/timetracker"]
