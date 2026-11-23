// Package listener opens the server's listening socket.
package listener

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/coreos/go-systemd/v22/activation"
)

// Listen returns the socket systemd passed in (socket activation), or else a
// TCP listener on addr. With neither, it fails. On cardea the unit gets /run/sandboxed/stockpot.sock,
// which only Caddy may connect to, so the process never binds anything.
func Listen(ctx context.Context, addr string) (net.Listener, error) {
	lns, err := activation.Listeners()
	if err != nil {
		return nil, fmt.Errorf("socket activation: %w", err)
	}
	switch len(lns) {
	case 0:
		if addr == "" {
			return nil, errors.New("no socket from systemd and STOCKPOT_LISTEN is unset")
		}
		var lc net.ListenConfig
		ln, err := lc.Listen(ctx, "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("listen on %s: %w", addr, err)
		}
		return ln, nil
	case 1:
		return lns[0], nil
	default:
		for _, ln := range lns {
			_ = ln.Close()
		}
		return nil, fmt.Errorf("socket activation: got %d sockets, want 1", len(lns))
	}
}
