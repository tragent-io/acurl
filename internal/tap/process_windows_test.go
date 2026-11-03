package tap

import (
	"context"
	"encoding/binary"
	"errors"
	"net/netip"
	"os"
	"strconv"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsTCPTablePIDIPv4(t *testing.T) {
	server := netip.MustParseAddrPort("127.0.0.1:8080")
	client := netip.MustParseAddrPort("127.0.0.1:54321")
	otherServer := netip.MustParseAddrPort("127.0.0.2:8080")
	table := windowsTestTable(
		windowsTestTCP4Row(server, client, 100), // The proxy's accepted socket.
		windowsTestTCP4Row(client, otherServer, 200),
		windowsTestTCP4Row(client, netip.AddrPortFrom(server.Addr(), 8081), 300),
		windowsTestTCP4Row(netip.AddrPortFrom(client.Addr(), 54322), server, 400),
		windowsTestTCP4Row(client, server, 0),
		windowsTestTCP4Row(client, server, 0x01020304),
	)
	pid, err := windowsTCPTablePID(table, windows.AF_INET, server, client)
	if err != nil || pid != 0x01020304 {
		t.Fatalf("got PID %d, error %v", pid, err)
	}
	_, err = windowsTCPTablePID(windowsTestTable(windowsTestTCP4Row(server, client, 100)), windows.AF_INET, server, client)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reverse row must not match: %v", err)
	}
}

func TestWindowsTCPTablePIDIPv6(t *testing.T) {
	for _, tc := range []struct {
		name   string
		server string
		client string
	}{
		{"loopback", "[::1]:8080", "[::1]:54321"},
		{"scoped", "[fe80::1234%16909060]:8080", "[fe80::abcd%16909060]:54321"},
		{"mapped", "127.0.0.1:8080", "127.0.0.1:54321"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := netip.MustParseAddrPort(tc.server)
			client := netip.MustParseAddrPort(tc.client)
			table := windowsTestTable(windowsTestTCP6Row(server, client, 100), windowsTestTCP6Row(client, server, 0x01020304))
			pid, err := windowsTCPTablePID(table, windows.AF_INET6, server, client)
			if err != nil || pid != 0x01020304 {
				t.Fatalf("got PID %d, error %v", pid, err)
			}
			if client.Addr().Zone() != "" {
				wrongZone := netip.AddrPortFrom(client.Addr().WithZone("4"), client.Port())
				if _, err := windowsTCPTablePID(table, windows.AF_INET6, server, wrongZone); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("wrong scope matched: %v", err)
				}
			}
		})
	}
}

func TestWindowsTCPTablePIDBounds(t *testing.T) {
	server := netip.MustParseAddrPort("127.0.0.1:8080")
	client := netip.MustParseAddrPort("127.0.0.1:54321")
	for _, family := range []uint32{windows.AF_INET, windows.AF_INET6} {
		t.Run(strconv.Itoa(int(family)), func(t *testing.T) {
			row := windowsTestTCP4Row(client, server, 123)
			if family == windows.AF_INET6 {
				row = windowsTestTCP6Row(client, server, 123)
			}
			complete := windowsTestTable(row)
			for length := range len(complete) {
				if _, err := windowsTCPTablePID(complete[:length], family, server, client); err == nil {
					t.Fatalf("accepted truncated table with %d bytes", length)
				}
			}
			tooMany := append([]byte(nil), complete...)
			binary.LittleEndian.PutUint32(tooMany, ^uint32(0))
			if _, err := windowsTCPTablePID(tooMany, family, server, client); err == nil {
				t.Fatal("accepted overflowing row count")
			}
			if _, err := windowsTCPTablePID([]byte{0, 0, 0, 0}, family, server, client); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("empty table: %v", err)
			}
		})
	}
	if _, err := windowsTCPTablePID([]byte{0, 0, 0, 0}, 999, server, client); err == nil {
		t.Fatal("accepted unsupported family")
	}
}

func TestWindowsNumericZone(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
	}{
		{"[::ffff:127.0.0.1]:8080", "127.0.0.1:8080"},
		{"[fe80::1%0002]:8080", "[fe80::1%2]:8080"},
		{"[::1%0]:8080", "[::1]:8080"},
	} {
		got, err := windowsNumericZone(netip.MustParseAddrPort(tc.input))
		if err != nil || got.String() != tc.want {
			t.Errorf("%s: got %s, error %v; want %s", tc.input, got, err, tc.want)
		}
	}
	if _, err := windowsNumericZone(netip.AddrPort{}); err == nil {
		t.Fatal("accepted invalid endpoint")
	}
}

func TestWindowsLookupProcessCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := lookupProcess(ctx, netip.MustParseAddrPort("127.0.0.1:8080"), netip.MustParseAddrPort("127.0.0.1:54321"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v; want canceled context", err)
	}
}

func windowsTestTable(rows ...[]byte) []byte {
	table := make([]byte, 4)
	binary.LittleEndian.PutUint32(table, uint32(len(rows)))
	for _, row := range rows {
		table = append(table, row...)
	}
	return table
}

func windowsTestTCP4Row(local, remote netip.AddrPort, pid uint32) []byte {
	row := make([]byte, 24)
	binary.LittleEndian.PutUint32(row, 5) // MIB_TCP_STATE_ESTAB.
	localAddr, remoteAddr := local.Addr().As4(), remote.Addr().As4()
	copy(row[4:8], localAddr[:])
	binary.BigEndian.PutUint16(row[8:10], local.Port())
	copy(row[12:16], remoteAddr[:])
	binary.BigEndian.PutUint16(row[16:18], remote.Port())
	binary.LittleEndian.PutUint32(row[20:24], pid)
	return row
}

func windowsTestTCP6Row(local, remote netip.AddrPort, pid uint32) []byte {
	row := make([]byte, 56)
	windowsTestTCP6Endpoint(row[:24], local)
	windowsTestTCP6Endpoint(row[24:48], remote)
	binary.LittleEndian.PutUint32(row[48:52], 5)
	binary.LittleEndian.PutUint32(row[52:56], pid)
	return row
}

func windowsTestTCP6Endpoint(dst []byte, endpoint netip.AddrPort) {
	addr := endpoint.Addr().As16()
	copy(dst[:16], addr[:])
	if zone := endpoint.Addr().Zone(); zone != "" {
		index, _ := strconv.ParseUint(zone, 10, 32)
		binary.BigEndian.PutUint32(dst[16:20], uint32(index))
	}
	binary.BigEndian.PutUint16(dst[20:22], endpoint.Port())
}
