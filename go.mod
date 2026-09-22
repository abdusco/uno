module unoparty

go 1.22

require (
	github.com/coder/websocket v1.8.12
	github.com/labstack/echo/v4 v4.11.4
	github.com/stretchr/testify v1.12.1
)

require (
	github.com/golang-jwt/jwt v3.2.2+incompatible // indirect
	github.com/labstack/gommon v0.4.2 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/valyala/bytebufferpool v1.0.0 // indirect
	github.com/valyala/fasttemplate v1.2.2 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/crypto v0.17.0 // indirect
	golang.org/x/net v0.19.0 // indirect
	golang.org/x/sys v0.15.0 // indirect
	golang.org/x/text v0.14.0 // indirect
	golang.org/x/time v0.5.0 // indirect
)

replace (
	golang.org/x/crypto => github.com/golang/crypto v0.17.0
	golang.org/x/net => github.com/golang/net v0.19.0
	golang.org/x/sys => github.com/golang/sys v0.15.0
	golang.org/x/term => github.com/golang/term v0.15.0
	golang.org/x/text => github.com/golang/text v0.14.0
	golang.org/x/time => github.com/golang/time v0.5.0
)
