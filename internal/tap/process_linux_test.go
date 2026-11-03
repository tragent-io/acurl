package tap

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLinuxEndpoint(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"0100007F:C350", "127.0.0.1:50000"},
		{"00000000000000000000000001000000:1F90", "[::1]:8080"},
		{"0000000000000000FFFF00000100007F:C350", "127.0.0.1:50000"},
	} {
		got, err := parseLinuxEndpoint(tc.input)
		if err != nil || got.String() != tc.want {
			t.Errorf("parseLinuxEndpoint(%q) = %v, %v; want %s", tc.input, got, err, tc.want)
		}
	}
	for _, value := range []string{"", "not-ip:1234", "0100007F:zzzz", "0100007F:12345"} {
		if _, err := parseLinuxEndpoint(value); err == nil {
			t.Errorf("accepted invalid endpoint %q", value)
		}
	}
}

func TestLinuxSocketInodes(t *testing.T) {
	local := netip.MustParseAddrPort("127.0.0.1:8080")
	remote := netip.MustParseAddrPort("[::ffff:127.0.0.1]:50000")
	fixture := ` sl local_address rem_address st tx_queue rx tr tm retr uid timeout inode
 0: 0100007F:C350 0100007F:1F90 01 0 0 0 0 0 123
 1: 0100007F:1F90 0100007F:C350 01 0 0 0 0 0 456
 2: 0100007F:C350 0200007F:1F90 01 0 0 0 0 0 789
 3: 0100007F:C350 0100007F:1F90 0A 0 0 0 0 0 999
 malformed
 4: invalid 0100007F:1F90 01 0 0 0 0 0 100
`
	got, err := linuxSocketInodes(context.Background(), strings.NewReader(fixture), local, remote)
	if err != nil || len(got) != 1 || !got["123"] {
		t.Fatalf("inodes = %v, %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := linuxSocketInodes(ctx, strings.NewReader(fixture), local, remote); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled scan returned %v", err)
	}
}

func TestLookupLinuxProcessUnavailable(t *testing.T) {
	local := netip.MustParseAddrPort("127.0.0.1:8080")
	remote := netip.MustParseAddrPort("127.0.0.1:50000")
	root := t.TempDir()
	if _, err := lookupLinuxProcess(context.Background(), root, local, remote); err == nil {
		t.Fatal("missing tables should fail")
	}
	if err := os.MkdirAll(filepath.Join(root, "net"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "net", "tcp"), []byte("0: 0100007F:C350 0100007F:1F90 01 0 0 0 0 0 123\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := lookupLinuxProcess(context.Background(), root, local, remote); err == nil {
		t.Fatal("missing owner should fail")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := lookupLinuxProcess(ctx, root, local, remote); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lookup returned %v", err)
	}
}

func TestLookupLinuxProcessOwner(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"net", "42/fd", "43"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "net/tcp"), []byte("0: 0100007F:C350 0100007F:1F90 01 0 0 0 0 0 123\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("socket:[123]", filepath.Join(root, "42/fd/5")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "42/comm"), []byte("client\n"), 0600); err != nil {
		t.Fatal(err)
	}
	local := netip.MustParseAddrPort("127.0.0.1:8080")
	remote := netip.MustParseAddrPort("127.0.0.1:50000")
	got, err := lookupLinuxProcess(context.Background(), root, local, remote)
	if err != nil || got.pid != 42 || got.name != "client" {
		t.Fatalf("owner = %+v, %v", got, err)
	}
	if err := os.Symlink("/usr/bin/actual (deleted)", filepath.Join(root, "42/exe")); err != nil {
		t.Fatal(err)
	}
	got, err = lookupLinuxProcess(context.Background(), root, local, remote)
	if err != nil || got.name != "actual" {
		t.Fatalf("executable owner = %+v, %v", got, err)
	}
	if err := os.MkdirAll(filepath.Join(root, "44/fd"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("socket:[123]", filepath.Join(root, "44/fd/5")); err != nil {
		t.Fatal(err)
	}
	if _, err := lookupLinuxProcess(context.Background(), root, local, remote); err == nil {
		t.Fatal("multiple owners should fail")
	}
}
