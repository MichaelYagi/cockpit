module github.com/MichaelYagi/cockpit

go 1.27

toolchain go1.27.4

require (
	github.com/getkin/kin-openapi v0.153.0
	github.com/go-chi/chi/v5 v5.5.0
	go.uber.org/goleak v1.3.0
	go.yaml.in/yaml/v3 v3.0.6
	golang.org/x/sys v0.50.0
)

require (
	github.com/BurntSushi/toml v1.4.1-0.20240526193622-a339e1f7089c // indirect
	github.com/go-openapi/jsonpointer v0.22.5 // indirect
	github.com/go-openapi/swag/jsonname v0.25.5 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3 // indirect
	go.yaml.in/yaml/v4 v4.0.0 // indirect
	golang.org/x/exp/typeparams v0.0.0-20231108232855-2478ac86f678 // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/telemetry v0.0.0-20261010193011-f71dc92da45e // indirect
	golang.org/x/text v0.44.0 // indirect
	golang.org/x/tools v0.52.0 // indirect
	golang.org/x/vuln v1.9.0 // indirect
	honnef.co/go/tools v0.8.2 // indirect
)

tool (
	golang.org/x/vuln/cmd/govulncheck
	honnef.co/go/tools/cmd/staticcheck
)
