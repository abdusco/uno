# UNO Party

A lightweight, realtime multiplayer UNO game that runs as an installable PWA.
Create a room, share its link or QR code, and play together from any browser.

## Run it

```sh
go run .
```

Open [http://localhost:8080](http://localhost:8080). Set `PORT` to use a
different port.

For frontend iteration without rebuilding the Go binary, run:

```sh
DEBUG=1 go run .
```

## Notes

- No database is used: rooms and games live only in memory and disappear when
  the server restarts.
- Players reconnect automatically while a room is still live. If a restart has
  removed the room, the app returns to the create/join screen.
- The frontend is bundled into the Go binary for deployment, with no CDN
  dependencies.

## Verify

```sh
go test ./...
go vet ./...
go build ./...
```
