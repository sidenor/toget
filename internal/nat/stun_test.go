package nat

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func TestDiscoverSTUN(t *testing.T) {
	server := startSTUNServer(t, net.IPv4(203, 0, 113, 7), 54321, false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ip, port, err := discoverSTUN(ctx, server, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !ip.Equal(net.IPv4(203, 0, 113, 7)) || port != 54321 {
		t.Fatalf("got %s:%d", ip, port)
	}
}

func TestDiscoverSTUNRejectsWrongTransaction(t *testing.T) {
	server := startSTUNServer(t, net.IPv4(203, 0, 113, 7), 54321, true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, err := discoverSTUN(ctx, server, 300*time.Millisecond); err == nil {
		t.Fatal("response with a different transaction id was accepted")
	}
}

func TestNormalizeSTUNServer(t *testing.T) {
	cases := map[string]string{
		"stun.example.com":       "stun.example.com:3478",
		"stun.example.com:19302": "stun.example.com:19302",
		"192.0.2.1":              "192.0.2.1:3478",
		"[2001:db8::1]":          "[2001:db8::1]:3478",
	}
	for input, want := range cases {
		got, err := normalizeSTUNServer(input)
		if err != nil {
			t.Fatalf("%s: %v", input, err)
		}
		if got != want {
			t.Fatalf("%s = %q, want %q", input, got, want)
		}
	}
	if _, err := normalizeSTUNServer(""); err == nil {
		t.Fatal("empty STUN server accepted")
	}
}

func startSTUNServer(t *testing.T, mapped net.IP, port int, wrongTransaction bool) string {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n < stunHeaderSize || binary.BigEndian.Uint16(buf[0:2]) != stunBindingRequest {
				continue
			}
			transaction := append([]byte(nil), buf[8:20]...)
			if wrongTransaction {
				transaction[0] ^= 0xff
			}
			value := make([]byte, 8)
			value[1] = 0x01
			binary.BigEndian.PutUint16(value[2:4], uint16(port)^uint16(stunMagicCookie>>16))
			copy(value[4:8], mapped.To4())
			cookie := make([]byte, 4)
			binary.BigEndian.PutUint32(cookie, stunMagicCookie)
			for i := 0; i < 4; i++ {
				value[4+i] ^= cookie[i]
			}
			response := make([]byte, stunHeaderSize+12)
			binary.BigEndian.PutUint16(response[0:2], stunBindingSuccess)
			binary.BigEndian.PutUint16(response[2:4], 12)
			binary.BigEndian.PutUint32(response[4:8], stunMagicCookie)
			copy(response[8:20], transaction)
			binary.BigEndian.PutUint16(response[20:22], stunAttrXORMapped)
			binary.BigEndian.PutUint16(response[22:24], 8)
			copy(response[24:32], value)
			_, _ = conn.WriteToUDP(response, addr)
		}
	}()
	return conn.LocalAddr().String()
}
