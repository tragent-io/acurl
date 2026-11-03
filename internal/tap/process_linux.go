package tap

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func lookupProcess(ctx context.Context, local, remote netip.AddrPort) (processIdentity, error) {
	return lookupLinuxProcess(ctx, "/proc", local, remote)
}

func lookupLinuxProcess(ctx context.Context, root string, local, remote netip.AddrPort) (processIdentity, error) {
	inodes := make(map[string]bool)
	for _, table := range []string{"tcp", "tcp6"} {
		if err := ctx.Err(); err != nil {
			return processIdentity{}, err
		}
		f, err := os.Open(filepath.Join(root, "net", table))
		if err != nil {
			continue
		}
		found, err := linuxSocketInodes(ctx, f, local, remote)
		_ = f.Close()
		if err != nil {
			return processIdentity{}, err
		}
		for inode := range found {
			inodes[inode] = true
		}
	}
	if len(inodes) == 0 {
		return processIdentity{}, errors.New("client socket not found in proc")
	}
	processes, err := os.ReadDir(root)
	if err != nil {
		return processIdentity{}, err
	}
	var owner processIdentity
	for _, process := range processes {
		if err := ctx.Err(); err != nil {
			return processIdentity{}, err
		}
		pid, err := strconv.Atoi(process.Name())
		if err != nil || pid <= 0 || !process.IsDir() {
			continue
		}
		base := filepath.Join(root, process.Name())
		fds, err := os.ReadDir(filepath.Join(base, "fd"))
		if err != nil {
			continue // Processes may exit or deny access during the scan.
		}
		for _, fd := range fds {
			if err := ctx.Err(); err != nil {
				return processIdentity{}, err
			}
			target, err := os.Readlink(filepath.Join(base, "fd", fd.Name()))
			if err != nil || !strings.HasPrefix(target, "socket:[") || !strings.HasSuffix(target, "]") {
				continue
			}
			if !inodes[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] {
				continue
			}
			if owner.pid != 0 && owner.pid != pid {
				return processIdentity{}, errors.New("client socket has multiple process owners")
			}
			name := ""
			if exe, err := os.Readlink(filepath.Join(base, "exe")); err == nil {
				name = filepath.Base(strings.TrimSuffix(exe, " (deleted)"))
			} else if comm, err := os.ReadFile(filepath.Join(base, "comm")); err == nil {
				name = strings.TrimSpace(string(comm))
			}
			if name == "" {
				return processIdentity{}, errors.New("client process name unavailable")
			}
			owner = processIdentity{name: name, pid: pid}
			break
		}
	}
	if owner.pid == 0 {
		return processIdentity{}, errors.New("client socket owner unavailable")
	}
	return owner, nil
}

func linuxSocketInodes(ctx context.Context, r io.Reader, local, remote netip.AddrPort) (map[string]bool, error) {
	inodes := make(map[string]bool)
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 || fields[3] != "01" {
			continue
		}
		client, err := parseLinuxEndpoint(fields[1])
		if err != nil {
			continue
		}
		peer, err := parseLinuxEndpoint(fields[2])
		if err != nil || client != unmapLinuxEndpoint(remote) || peer != unmapLinuxEndpoint(local) {
			continue
		}
		if inode, err := strconv.ParseUint(fields[9], 10, 64); err == nil && inode != 0 {
			inodes[fields[9]] = true
		}
	}
	return inodes, scanner.Err()
}

func unmapLinuxEndpoint(endpoint netip.AddrPort) netip.AddrPort {
	return netip.AddrPortFrom(endpoint.Addr().Unmap(), endpoint.Port())
}

func parseLinuxEndpoint(value string) (netip.AddrPort, error) {
	address, port, ok := strings.Cut(value, ":")
	if !ok || (len(address) != 8 && len(address) != 32) || len(port) != 4 {
		return netip.AddrPort{}, fmt.Errorf("invalid proc endpoint %q", value)
	}
	b, err := hex.DecodeString(address)
	if err != nil {
		return netip.AddrPort{}, err
	}
	// proc renders each native uint32 in host byte order, including each of
	// the four IPv6 words.
	for i := 0; i < len(b); i += 4 {
		binary.BigEndian.PutUint32(b[i:i+4], binary.NativeEndian.Uint32(b[i:i+4]))
	}
	ip, _ := netip.AddrFromSlice(b)
	p, err := strconv.ParseUint(port, 16, 16)
	if err != nil {
		return netip.AddrPort{}, err
	}
	return netip.AddrPortFrom(ip.Unmap(), uint16(p)), nil
}
