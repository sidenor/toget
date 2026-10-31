package nat

import (
	"context"
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"
)

type fakePCPGateway struct {
	conn       *net.UDPConn
	result     byte
	externalIP net.IP
	delta      int

	mu       sync.Mutex
	requests [][]byte
}

func startPCPGateway(t *testing.T, result byte, externalIP net.IP, delta int) *fakePCPGateway {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	gateway := &fakePCPGateway{conn: conn, result: result, externalIP: externalIP, delta: delta}
	t.Cleanup(func() { _ = conn.Close() })
	go gateway.serve()
	return gateway
}

func (g *fakePCPGateway) addr() string { return g.conn.LocalAddr().String() }

func (g *fakePCPGateway) lastRequest() []byte {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.requests) == 0 {
		return nil
	}
	return append([]byte(nil), g.requests[len(g.requests)-1]...)
}

func (g *fakePCPGateway) serve() {
	buf := make([]byte, 128)
	for {
		n, addr, err := g.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		request := append([]byte(nil), buf[:n]...)
		g.mu.Lock()
		g.requests = append(g.requests, request)
		g.mu.Unlock()
		if request[0] == natpmpVersion {
			if response := natpmpResponse(request); response != nil {
				_, _ = g.conn.WriteToUDP(response, addr)
			}
			continue
		}
		if n < pcpRequestSize {
			continue
		}
		if n < pcpRequestSize || request[0] != pcpVersion || request[1] != pcpOpMap {
			continue
		}
		response := make([]byte, pcpResponseSize)
		response[0] = pcpVersion
		response[1] = pcpOpMapResponse
		response[3] = g.result
		copy(response[4:8], request[4:8])
		copy(response[24:36], request[24:36])
		response[36] = request[36]
		copy(response[40:42], request[40:42])
		externalPort := int(binary.BigEndian.Uint16(request[42:44])) + g.delta
		binary.BigEndian.PutUint16(response[42:44], uint16(externalPort))
		if g.externalIP != nil {
			copy(response[44:60], g.externalIP.To16())
		}
		_, _ = g.conn.WriteToUDP(response, addr)
	}
}

// natpmpResponse handles the NAT-PMP fallback on the same fake gateway: it
// maps UDP and reports a public address so a mixed-method test stays local.
func natpmpResponse(request []byte) []byte {
	if len(request) < 2 {
		return nil
	}
	switch request[1] {
	case natpmpOpPublicIP:
		response := make([]byte, 12)
		response[0] = natpmpVersion
		response[1] = natpmpRespPublicIP
		copy(response[8:12], net.IPv4(198, 51, 100, 9).To4())
		return response
	case natpmpOpMapUDP:
		if len(request) < 12 {
			return nil
		}
		response := make([]byte, 16)
		response[0] = natpmpVersion
		response[1] = natpmpRespMapUDP
		response[4] = 1
		copy(response[8:10], request[4:6])
		copy(response[10:12], request[6:8])
		copy(response[12:16], request[8:12])
		return response
	case natpmpOpDeleteUDP:
		response := make([]byte, 16)
		response[0] = natpmpVersion
		response[1] = natpmpRespDeleteUDP
		return response
	default:
		return nil
	}
}

func newTestManager(t *testing.T, gateway string, methods ...string) *Manager {
	t.Helper()
	manager, err := New(Options{Gateway: gateway, Methods: methods, Timeout: 150 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestPCPMapTCPAndUDP(t *testing.T) {
	gateway := startPCPGateway(t, 0, net.IPv4(198, 51, 100, 10), 500)
	manager := newTestManager(t, gateway.addr(), MethodPCP)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, protocol := range []Protocol{TCP, UDP} {
		mapping, err := manager.Map(ctx, protocol, 7717, 7717, time.Hour)
		if err != nil {
			t.Fatalf("%s: %v", protocol, err)
		}
		if mapping.Method != MethodPCP || mapping.Protocol != protocol ||
			mapping.InternalPort != 7717 || mapping.ExternalPort != 8217 ||
			!mapping.ExternalIP.Equal(net.IPv4(198, 51, 100, 10)) {
			t.Fatalf("%s mapping = %#v", protocol, mapping)
		}
		if mapping.Lifetime != time.Hour {
			t.Fatalf("%s lifetime = %s", protocol, mapping.Lifetime)
		}
	}
}

func TestPCPUnmapSendsZeroLifetime(t *testing.T) {
	gateway := startPCPGateway(t, 0, net.IPv4(198, 51, 100, 10), 0)
	manager := newTestManager(t, gateway.addr(), MethodPCP)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := manager.Map(ctx, UDP, 7717, 7717, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := manager.Unmap(ctx, Mapping{Method: MethodPCP, Protocol: UDP, InternalPort: 7717, ExternalPort: 7717}); err != nil {
		t.Fatal(err)
	}
	last := gateway.lastRequest()
	if len(last) < pcpRequestSize || binary.BigEndian.Uint32(last[4:8]) != 0 {
		t.Fatalf("unmap did not send a zero lifetime: % x", last)
	}
}

func TestPCPServerError(t *testing.T) {
	gateway := startPCPGateway(t, 8, nil, 0)
	manager := newTestManager(t, gateway.addr(), MethodPCP)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := manager.Map(ctx, UDP, 7717, 7717, time.Hour); err == nil {
		t.Fatal("PCP no-resources error was ignored")
	}
}

func TestPCPFallsBackToNATPMP(t *testing.T) {
	gateway := startPCPGateway(t, 1, nil, 0) // unsupported version
	manager := newTestManager(t, gateway.addr(), MethodPCP, MethodNATPMP)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	mapping, err := manager.Map(ctx, UDP, 7717, 7717, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if mapping.Method != MethodNATPMP || mapping.ExternalPort != 7717 {
		t.Fatalf("mapping = %#v", mapping)
	}
	if !mapping.ExternalIP.Equal(net.IPv4(198, 51, 100, 9)) {
		t.Fatalf("external IP = %s", mapping.ExternalIP)
	}
}
