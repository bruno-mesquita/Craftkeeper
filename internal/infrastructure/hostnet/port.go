// Package hostnet answers questions about the host's network.
package hostnet

import (
	"net"
	"strconv"
)

// TCPPortFree reports whether port can be bound on every interface right now. It is a hint
// for choosing a port (another process may take it afterwards); Docker reports the real
// conflict when the container starts.
func TCPPortFree(port int) bool {
	ln, err := net.Listen("tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}
