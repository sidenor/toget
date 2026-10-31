package nat

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const fakeUPnPDescription = `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <URLBase>%s</URLBase>
  <device>
    <deviceType>urn:schemas-upnp-org:device:InternetGatewayDevice:1</deviceType>
    <friendlyName>Test Gateway</friendlyName>
    <deviceList>
      <device>
        <deviceType>urn:schemas-upnp-org:device:WANDevice:1</deviceType>
        <deviceList>
          <device>
            <deviceType>urn:schemas-upnp-org:device:WANConnectionDevice:1</deviceType>
            <serviceList>
              <service>
                <serviceType>urn:schemas-upnp-org:service:WANIPConnection:1</serviceType>
                <controlURL>/control</controlURL>
              </service>
            </serviceList>
          </device>
        </deviceList>
      </device>
    </deviceList>
  </device>
</root>`

type fakeUPnP struct {
	externalIP string
	failCode   string
	mu         sync.Mutex
	actions    []string
	lastBody   string
	addBody    string
}

func startFakeUPnP(t *testing.T, externalIP, failCode string) (*httptest.Server, *fakeUPnP) {
	t.Helper()
	gateway := &fakeUPnP{externalIP: externalIP, failCode: failCode}
	mux := http.NewServeMux()
	mux.HandleFunc("/description.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprintf(w, fakeUPnPDescription, "http://"+r.Host+"/")
	})
	mux.HandleFunc("/control", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		action := r.Header.Get("SOAPAction")
		gateway.mu.Lock()
		gateway.actions = append(gateway.actions, action)
		gateway.lastBody = string(body)
		if strings.Contains(action, "AddPortMapping") {
			gateway.addBody = string(body)
		}
		gateway.mu.Unlock()
		if gateway.failCode != "" {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault><detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>%s</errorCode></UPnPError></detail></s:Fault></s:Body></s:Envelope>`, gateway.failCode)
			return
		}
		w.Header().Set("Content-Type", "text/xml")
		if strings.Contains(action, "GetExternalIPAddress") {
			fmt.Fprintf(w, `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:GetExternalIPAddressResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1"><NewExternalIPAddress>%s</NewExternalIPAddress></u:GetExternalIPAddressResponse></s:Body></s:Envelope>`, gateway.externalIP)
			return
		}
		_, _ = io.WriteString(w, `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:Response xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1"/></s:Body></s:Envelope>`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, gateway
}

func TestUPnPMapAndUnmap(t *testing.T) {
	server, gateway := startFakeUPnP(t, "203.0.113.44", "")
	manager, err := New(Options{
		Methods: []string{MethodUPnP}, UPnPLocation: server.URL + "/description.xml",
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mapping, err := manager.Map(ctx, TCP, 7717, 7717, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if mapping.Method != MethodUPnP || mapping.Protocol != TCP || mapping.ExternalPort != 7717 {
		t.Fatalf("mapping = %#v", mapping)
	}
	if !mapping.ExternalIP.Equal(net.ParseIP("203.0.113.44")) {
		t.Fatalf("external IP = %s", mapping.ExternalIP)
	}
	gateway.mu.Lock()
	if len(gateway.actions) < 2 || !strings.Contains(gateway.actions[0], "AddPortMapping") {
		t.Fatalf("actions = %v", gateway.actions)
	}
	if !strings.Contains(gateway.addBody, "<NewInternalClient>127.0.0.1</NewInternalClient>") {
		t.Fatalf("mapping body = %s", gateway.addBody)
	}
	gateway.mu.Unlock()

	if err := manager.Unmap(ctx, mapping); err != nil {
		t.Fatal(err)
	}
	gateway.mu.Lock()
	last := gateway.actions[len(gateway.actions)-1]
	gateway.mu.Unlock()
	if !strings.Contains(last, "DeletePortMapping") {
		t.Fatalf("last action = %s", last)
	}
}

func TestUPnPErrorResponse(t *testing.T) {
	server, _ := startFakeUPnP(t, "203.0.113.44", "718")
	manager, err := New(Options{Methods: []string{MethodUPnP}, UPnPLocation: server.URL + "/description.xml"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = manager.Map(ctx, UDP, 7717, 7717, time.Hour)
	if err == nil || !strings.Contains(err.Error(), "718") {
		t.Fatalf("error = %v, want UPnP code 718", err)
	}
}

func TestUPnPMissingService(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<root><device><deviceType>urn:schemas-upnp-org:device:InternetGatewayDevice:1</deviceType></device></root>`)
	}))
	defer server.Close()
	manager, err := New(Options{Methods: []string{MethodUPnP}, UPnPLocation: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := manager.Map(ctx, TCP, 7717, 7717, time.Hour); err == nil {
		t.Fatal("description without a WAN connection service was accepted")
	}
}

func TestUPnPRejectsRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer server.Close()
	manager, err := New(Options{Methods: []string{MethodUPnP}, UPnPLocation: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := manager.Map(ctx, TCP, 7717, 7717, time.Hour); err == nil {
		t.Fatal("redirected description was accepted")
	}
}

func TestSSDPLocation(t *testing.T) {
	message := []byte("HTTP/1.1 200 OK\r\nST: urn:schemas-upnp-org:device:InternetGatewayDevice:1\r\nLOCATION: http://192.168.1.1:5000/rootDesc.xml\r\n\r\n")
	location, ok := ssdpLocation(message)
	if !ok || location != "http://192.168.1.1:5000/rootDesc.xml" {
		t.Fatalf("location = %q, %v", location, ok)
	}
	if _, ok := ssdpLocation([]byte("HTTP/1.1 200 OK\r\n\r\n")); ok {
		t.Fatal("missing LOCATION was accepted")
	}
}
