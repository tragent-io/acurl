package tap

import (
	"context"
	"errors"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
)

func lookupProcess(ctx context.Context, local, remote netip.AddrPort) (processIdentity, error) {
	// Select the client's local port, then verify the complete connection below.
	output, err := exec.CommandContext(ctx, "/usr/sbin/lsof", "+c", "0", "-nP", "-a",
		"-iTCP:"+strconv.Itoa(int(remote.Port())), "-sTCP:ESTABLISHED", "-F0pcfn").Output()
	if err != nil {
		return processIdentity{}, err
	}
	return parseDarwinProcess(output, local, remote)
}

func parseDarwinProcess(output []byte, local, remote netip.AddrPort) (processIdentity, error) {
	var current, owner processIdentity
	file := false
	for _, field := range strings.Split(string(output), "\x00") {
		// Even with -F0, lsof terminates each process/file set with a newline.
		field = strings.TrimLeft(field, "\n")
		if field == "" {
			continue
		}
		switch field[0] {
		case 'p':
			pid, _ := strconv.Atoi(field[1:])
			current = processIdentity{pid: pid}
			file = false
		case 'c':
			current.name = field[1:]
		case 'f':
			file = true
		case 'n':
			clientText, peerText, ok := strings.Cut(field[1:], "->")
			if !ok || !file || current.pid <= 0 || current.name == "" {
				continue
			}
			client, err := netip.ParseAddrPort(clientText)
			if err != nil {
				continue
			}
			peer, err := netip.ParseAddrPort(peerText)
			if err != nil || !sameDarwinEndpoint(client, remote) || !sameDarwinEndpoint(peer, local) {
				continue
			}
			if owner.pid != 0 && owner.pid != current.pid {
				return processIdentity{}, errors.New("client socket has multiple process owners")
			}
			owner = current
		}
	}
	if owner.pid == 0 {
		return processIdentity{}, errors.New("client socket owner unavailable")
	}
	return owner, nil
}

func sameDarwinEndpoint(a, b netip.AddrPort) bool {
	return a.Port() == b.Port() && a.Addr().Unmap() == b.Addr().Unmap()
}
