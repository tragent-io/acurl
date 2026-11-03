//go:build !linux && !darwin && !windows

package tap

import (
	"context"
	"errors"
	"net/netip"
)

func lookupProcess(context.Context, netip.AddrPort, netip.AddrPort) (processIdentity, error) {
	return processIdentity{}, errors.New("process discovery is not supported on this platform")
}
