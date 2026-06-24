// Package version exposes the bezalel build version and product identity in a
// single place so that the CLI, MCP server, HTTP user-agent, and LSP client
// handshake all report the same values.
package version

const (
	// Name is the product/server name reported to clients.
	Name = "bezalel"

	// Number is the semantic version of the bezalel binary.
	Number = "0.1.0"
)

// UserAgent is the HTTP User-Agent bezalel sends on outbound requests.
const UserAgent = Name + "/" + Number
