package oauth

import (
	"net/http"

	"github.com/Bigsy/mcpmu/internal/httpclient"
)

// newHTTPClient is the only way this package builds an HTTP client: same
// redirect policy as the MCP transport, deadlines from the request context.
func newHTTPClient() *http.Client {
	return httpclient.New(nil)
}
