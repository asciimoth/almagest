module github.com/asciimoth/almagest/bin

go 1.25.5

require (
	github.com/alexflint/go-arg v1.6.1
	github.com/asciimoth/almagest/lib v0.0.0-00010101000000-000000000000
	github.com/asciimoth/bufpool v0.3.0
	github.com/asciimoth/configer/configer v0.3.1
	github.com/asciimoth/configer/hujson v0.3.1
	github.com/asciimoth/formular v0.3.1
	github.com/asciimoth/gonnect v0.31.0
	github.com/asciimoth/gonnect-netstack v0.4.30
	github.com/asciimoth/pasta/pasta v0.9.1
	github.com/asciimoth/socksgo v0.4.0
	github.com/asciimoth/sysnet-linux v0.2.8
	github.com/coder/websocket v1.8.14
	github.com/joho/godotenv v1.5.1
	github.com/kataras/golog v0.1.15
	github.com/tailscale/hujson v0.0.0-20260302212456-ecc657c15afd
	golang.org/x/sys v0.44.0
)

require (
	github.com/alexflint/go-scalar v1.2.0 // indirect
	github.com/asciimoth/batchudp v0.3.0 // indirect
	github.com/asciimoth/gonnect-routing v0.7.2 // indirect
	github.com/asciimoth/ident v0.2.0 // indirect
	github.com/asciimoth/p-mark v0.2.0 // indirect
	github.com/asciimoth/persist v0.2.0 // indirect
	github.com/asciimoth/putback v0.3.0 // indirect
	github.com/asciimoth/tuntap v0.3.18 // indirect
	github.com/asciimoth/wgo v0.5.0 // indirect
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.1.1 // indirect
	github.com/cilium/ebpf v0.21.0 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/google/btree v1.1.2 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/nftables v0.3.0 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/mdlayher/netlink v1.7.3-0.20250113171957-fbb4dce95f42 // indirect
	github.com/mdlayher/socket v0.5.1 // indirect
	github.com/vishvananda/netlink v1.3.1 // indirect
	github.com/vishvananda/netns v0.0.5 // indirect
	github.com/wk8/go-ordered-map/v2 v2.1.8 // indirect
	github.com/xtaci/smux v1.5.44 // indirect
	golang.org/x/crypto v0.51.0 // indirect
	golang.org/x/exp v0.0.0-20231110203233-9a3e6036ecaa // indirect
	golang.org/x/net v0.54.0 // indirect
	golang.org/x/sync v0.17.0 // indirect
	golang.org/x/time v0.12.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	gvisor.dev/gvisor v0.0.0-20260325202830-7644cf3a343c // indirect
)

replace github.com/asciimoth/almagest/lib => ../lib
