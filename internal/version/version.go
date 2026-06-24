// Package version exposes the bezalel build version and product identity in a
// single place so that the CLI, MCP server, HTTP user-agent, and LSP client
// handshake all report the same values.
package version

// Name is the product/server name reported to clients.
const Name = "bezalel"

// Number is the semantic version of the bezalel binary. It defaults to a
// development marker and is overridden in release builds via:
//
//	-ldflags "-X github.com/aleksclark/bezalel/internal/version.Number=YYYYMM.DD.patch"
//
// The release format is CalVer (YYYYMM.DD.patch) which is also a valid SemVer
// major.minor.patch triple.
var Number = "0.0.0-dev"

// UserAgent returns the HTTP User-Agent bezalel sends on outbound requests.
func UserAgent() string {
	return Name + "/" + Number
}
