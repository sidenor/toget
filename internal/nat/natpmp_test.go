package nat

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func TestNATPMPSkipsTCP(t *testing.T) {
	gateway := startPCPGateway(t, 0, nil, 0)
	manager := newTestManager(t, gateway.addr(), MethodNATPMP)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := manager.Map(ctx, TCP, 7717, 7717, time.Hour); err == nil {
		t.Fatal("NAT-PMP mapped a TCP port it cannot forward")
	}
	if len(gateway.lastRequest()) != 0 {
		t.Fatal("NAT-PMP sent a request for TCP")
	}
}

func TestNATPMPServerError(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	go func() {
		buf := make([]byte, 64)
		for {
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n < 2 || buf[0] != natpmpVersion || buf[1] != natpmpOpMapUDP {
				continue
			}
			response := make([]byte, 16)
			response[0] = natpmpVersion
			response[1] = natpmpRespMapUDP
			binary.BigEndian.PutUint16(response[2:4], 3) // network failure
			_, _ = conn.WriteToUDP(response, addr)
		}
	}()
	manager := newTestManager(t, conn.LocalAddr().String(), MethodNATPMP)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := manager.Map(ctx, UDP, 7717, 7717, time.Hour); err == nil {
		t.Fatal("NAT-PMP gateway error was ignored")
	}
}

func TestGatewayHostPort(t *testing.T) {
	cases := map[string]string{
		"192.168.1.1":      "192.168.1.1:5351",
		"192.168.1.1:5352": "192.168.1.1:5352",
		"2001:db8::1":      "[2001:db8::1]:5351",
	}
	for input, want := range cases {
		got, err := gatewayHostPort(input)
		if err != nil {
			t.Fatalf("%s: %v", input, err)
		}
		if got != want {
			t.Fatalf("%s = %q, want %q", input, got, want)
		}
	}
	for _, input := range []string{"", "router.local", "192.168.1.1:bad"} {
		if _, err := gatewayHostPort(input); err == nil {
			t.Fatalf("%q was accepted as a gateway", input)
		}
	}
}
