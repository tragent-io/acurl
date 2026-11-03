package tap

import (
	"net/netip"
	"testing"
)

func TestParseDarwinProcess(t *testing.T) {
	for _, tc := range []struct {
		name, local, remote, output string
		wantPID                     int
	}{
		{"ipv4", "127.0.0.1:8080", "127.0.0.1:50000", "p42\x00cclient\x00\nf5\x00n127.0.0.1:50000->127.0.0.1:8080\x00\n", 42},
		{"ipv6", "[::1]:8080", "[::1]:50000", "p42\x00cclient\x00\nf5\x00n[::1]:50000->[::1]:8080\x00\n", 42},
		{"mapped", "127.0.0.1:8080", "127.0.0.1:50000", "p42\x00cclient\x00\nf5\x00n[::ffff:127.0.0.1]:50000->[::ffff:127.0.0.1]:8080\x00\n", 42},
		{"proxy direction", "127.0.0.1:8080", "127.0.0.1:50000", "p42\x00cclient\x00\nf5\x00n127.0.0.1:8080->127.0.0.1:50000\x00\n", 0},
		{"wrong peer", "127.0.0.1:8080", "127.0.0.1:50000", "p42\x00cclient\x00\nf5\x00n127.0.0.1:50000->127.0.0.2:8080\x00\n", 0},
		{"missing command", "127.0.0.1:8080", "127.0.0.1:50000", "p42\x00\nf5\x00n127.0.0.1:50000->127.0.0.1:8080\x00\n", 0},
		{"missing file", "127.0.0.1:8080", "127.0.0.1:50000", "p42\x00cclient\x00n127.0.0.1:50000->127.0.0.1:8080\x00", 0},
		{"malformed", "127.0.0.1:8080", "127.0.0.1:50000", "pbad\x00cclient\x00\nf5\x00ninvalid->invalid\x00\n", 0},
		{"empty", "127.0.0.1:8080", "127.0.0.1:50000", "", 0},
		{"ambiguous", "127.0.0.1:8080", "127.0.0.1:50000", "p42\x00cclient\x00\nf5\x00n127.0.0.1:50000->127.0.0.1:8080\x00\np43\x00cother\x00\nf6\x00n127.0.0.1:50000->127.0.0.1:8080\x00\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDarwinProcess([]byte(tc.output), netip.MustParseAddrPort(tc.local), netip.MustParseAddrPort(tc.remote))
			if tc.wantPID == 0 {
				if err == nil {
					t.Fatalf("expected unknown, got %+v", got)
				}
			} else if err != nil || got.pid != tc.wantPID || got.name != "client" {
				t.Fatalf("owner = %+v, %v", got, err)
			}
		})
	}
}
