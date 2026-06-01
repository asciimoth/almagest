module github.com/asciimoth/almagest/bin

go 1.25.5

require (
	github.com/alexflint/go-arg v1.6.1
	github.com/asciimoth/almagest/lib v0.0.0-00010101000000-000000000000
	github.com/asciimoth/configer/configer v0.3.0
	github.com/asciimoth/configer/hujson v0.3.0
	github.com/asciimoth/formular v0.2.2
	github.com/asciimoth/gonnect v0.16.0
	github.com/asciimoth/pasta/pasta v0.6.0
	github.com/coder/websocket v1.8.14
	github.com/joho/godotenv v1.5.1
	github.com/kataras/golog v0.1.15
)

require (
	github.com/alexflint/go-scalar v1.2.0 // indirect
	github.com/asciimoth/badlock v0.1.1 // indirect
	github.com/asciimoth/bufpool v0.3.0 // indirect
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.1.1 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/tailscale/hujson v0.0.0-20260302212456-ecc657c15afd // indirect
	github.com/wk8/go-ordered-map/v2 v2.1.8 // indirect
	golang.org/x/net v0.54.0 // indirect
	golang.org/x/sys v0.44.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/asciimoth/almagest/lib => ../lib
