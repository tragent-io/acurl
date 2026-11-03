package tap

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	getExtendedTCPTable = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")
	processKernel32     = windows.NewLazySystemDLL("kernel32.dll")
)

func lookupProcess(ctx context.Context, local, remote netip.AddrPort) (processIdentity, error) {
	if err := ctx.Err(); err != nil {
		return processIdentity{}, err
	}
	var err error
	local, err = windowsNumericZone(local)
	if err != nil {
		return processIdentity{}, err
	}
	remote, err = windowsNumericZone(remote)
	if err != nil {
		return processIdentity{}, err
	}
	if err := ctx.Err(); err != nil {
		return processIdentity{}, err
	}
	if err := getExtendedTCPTable.Find(); err != nil {
		return processIdentity{}, err
	}
	// A dual-stack socket can appear in the IPv6 table with mapped addresses.
	// Search both tables for IPv4 endpoints, matching the entire client tuple.
	families := []uint32{windows.AF_INET6}
	if local.Addr().Unmap().Is4() && remote.Addr().Unmap().Is4() {
		families = []uint32{windows.AF_INET, windows.AF_INET6}
	}
	var lookupErr error
	for _, family := range families {
		table, err := windowsTCPTable(ctx, family)
		if err != nil {
			lookupErr = err
			continue
		}
		pid, err := windowsTCPTablePID(table, family, local, remote)
		if err != nil {
			lookupErr = err
			continue
		}
		name, err := windowsProcessName(ctx, pid)
		if err != nil {
			return processIdentity{}, err
		}
		return processIdentity{name: name, pid: int(pid)}, nil
	}
	if err := ctx.Err(); err != nil {
		return processIdentity{}, err
	}
	return processIdentity{}, lookupErr
}

func windowsNumericZone(endpoint netip.AddrPort) (netip.AddrPort, error) {
	addr := endpoint.Addr()
	if !endpoint.IsValid() {
		return netip.AddrPort{}, errors.New("invalid TCP endpoint")
	}
	if zone := addr.Zone(); zone != "" {
		index, err := strconv.ParseUint(zone, 10, 32)
		if err != nil {
			iface, err := net.InterfaceByName(zone)
			if err != nil {
				return netip.AddrPort{}, err
			}
			index = uint64(iface.Index)
		}
		if index == 0 {
			addr = addr.WithZone("")
		} else {
			addr = addr.WithZone(strconv.FormatUint(index, 10))
		}
	}
	return netip.AddrPortFrom(addr.Unmap(), endpoint.Port()), nil
}

func windowsTCPTable(ctx context.Context, family uint32) ([]byte, error) {
	const maxTableSize = 16 << 20
	var table []byte
	var size uint32
	// The connection table can grow between the size query and the read.
	for range 4 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var ptr *byte
		if len(table) != 0 {
			ptr = &table[0]
		}
		// TCP_TABLE_OWNER_PID_CONNECTIONS (4) excludes listening sockets.
		status, _, _ := getExtendedTCPTable.Call(uintptr(unsafe.Pointer(ptr)), uintptr(unsafe.Pointer(&size)), 0, uintptr(family), 4, 0)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if status == 0 {
			if size < 4 || uint64(size) > uint64(len(table)) {
				return nil, errors.New("invalid TCP table size")
			}
			return table[:size], nil
		}
		if windows.Errno(status) != windows.ERROR_INSUFFICIENT_BUFFER {
			return nil, windows.Errno(status)
		}
		if size < 4 || size > maxTableSize || uint64(size) <= uint64(len(table)) {
			return nil, errors.New("invalid TCP table buffer size")
		}
		table = make([]byte, int(size))
	}
	return nil, errors.New("TCP table kept growing")
}

func windowsTCPTablePID(table []byte, family uint32, local, remote netip.AddrPort) (uint32, error) {
	rowSize := 24 // MIB_TCPROW_OWNER_PID: six DWORDs.
	if family == windows.AF_INET6 {
		rowSize = 56 // MIB_TCP6ROW_OWNER_PID.
	} else if family != windows.AF_INET {
		return 0, errors.New("unsupported TCP address family")
	}
	if len(table) < 4 {
		return 0, errors.New("truncated TCP table header")
	}
	count := binary.LittleEndian.Uint32(table[:4])
	if uint64(count) > uint64((len(table)-4)/rowSize) {
		return 0, errors.New("truncated TCP table rows")
	}
	for i := uint32(0); i < count; i++ {
		row := table[4+int(i)*rowSize : 4+(int(i)+1)*rowSize]
		var client, server netip.AddrPort
		var pid uint32
		if family == windows.AF_INET {
			client = netip.AddrPortFrom(netip.AddrFrom4([4]byte(row[4:8])), binary.BigEndian.Uint16(row[8:10]))
			server = netip.AddrPortFrom(netip.AddrFrom4([4]byte(row[12:16])), binary.BigEndian.Uint16(row[16:18]))
			pid = binary.LittleEndian.Uint32(row[20:24])
		} else {
			client = windowsTCP6Endpoint(row[:16], row[16:20], row[20:24])
			server = windowsTCP6Endpoint(row[24:40], row[40:44], row[44:48])
			pid = binary.LittleEndian.Uint32(row[52:56])
		}
		// The accepted proxy socket has the reversed tuple and belongs to us.
		if client == remote && server == local && pid != 0 {
			return pid, nil
		}
	}
	return 0, os.ErrNotExist
}

func windowsTCP6Endpoint(address, scope, port []byte) netip.AddrPort {
	addr := netip.AddrFrom16([16]byte(address)).Unmap()
	// The SDK specifies network byte order for both scope IDs and ports.
	if id := binary.BigEndian.Uint32(scope); id != 0 && addr.Is6() {
		addr = addr.WithZone(strconv.FormatUint(uint64(id), 10))
	}
	return netip.AddrPortFrom(addr, binary.BigEndian.Uint16(port[:2]))
}

func windowsProcessName(ctx context.Context, pid uint32) (string, error) {
	// Resolve explicitly: LazyProc.Call itself panics if an API is missing.
	for _, name := range []string{"OpenProcess", "QueryFullProcessImageNameW", "CloseHandle"} {
		if err := processKernel32.NewProc(name).Find(); err != nil {
			return "", err
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err == nil {
		defer func() { _ = windows.CloseHandle(handle) }()
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if err != nil {
		return "", err
	}
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	err = windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if err != nil {
		return "", err
	}
	if size == 0 || uint64(size) > uint64(len(buffer)) {
		return "", errors.New("invalid process image length")
	}
	name := filepath.Base(windows.UTF16ToString(buffer[:size]))
	if strings.EqualFold(filepath.Ext(name), ".exe") {
		name = name[:len(name)-4]
	}
	if name == "" || name == "." {
		return "", fmt.Errorf("empty process name for PID %d", pid)
	}
	return name, nil
}
