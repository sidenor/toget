package nat

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	upnpDiscoveryAddress = "239.255.255.250:1900"
	upnpMaxDescription   = 256 << 10
	upnpMaxResponse      = 128 << 10
	upnpSearchTarget     = "urn:schemas-upnp-org:device:InternetGatewayDevice:1"
	upnpMapDescription   = "Toget"
)

type upnpService struct {
	serviceType string
	controlURL  *url.URL
}

type upnpDescription struct {
	XMLName xml.Name   `xml:"root"`
	URLBase string     `xml:"URLBase"`
	Device  upnpDevice `xml:"device"`
}

type upnpDevice struct {
	DeviceType string           `xml:"deviceType"`
	Friendly   string           `xml:"friendlyName"`
	Services   []upnpServiceXML `xml:"serviceList>service"`
	Devices    []upnpDevice     `xml:"deviceList>device"`
}

type upnpServiceXML struct {
	ServiceType string `xml:"serviceType"`
	ControlURL  string `xml:"controlURL"`
}

// mapUPnP discovers an InternetGatewayDevice and adds a port mapping.
func (m *Manager) mapUPnP(ctx context.Context, protocol Protocol, internalPort, suggestedExternalPort int, lifetime time.Duration) (Mapping, error) {
	service, err := m.upnpService(ctx)
	if err != nil {
		return Mapping{}, err
	}
	internalIP, err := localIPForURL(service.controlURL)
	if err != nil {
		return Mapping{}, err
	}
	seconds := int(lifetime / time.Second)
	if seconds == 0 {
		seconds = 3600
	}
	args := []upnpArg{
		{Name: "NewRemoteHost", Value: ""},
		{Name: "NewExternalPort", Value: strconv.Itoa(suggestedExternalPort)},
		{Name: "NewProtocol", Value: strings.ToUpper(string(protocol))},
		{Name: "NewInternalPort", Value: strconv.Itoa(internalPort)},
		{Name: "NewInternalClient", Value: internalIP.String()},
		{Name: "NewEnabled", Value: "1"},
		{Name: "NewPortMappingDescription", Value: upnpMapDescription},
		{Name: "NewLeaseDuration", Value: strconv.Itoa(seconds)},
	}
	if _, err := m.upnpAction(ctx, service, "AddPortMapping", args); err != nil {
		return Mapping{}, err
	}
	mapping := Mapping{
		Method: MethodUPnP, Protocol: protocol,
		InternalPort: internalPort, ExternalPort: suggestedExternalPort,
		Lifetime: lifetime,
	}
	// GetExternalIPAddress is optional on some IGDs; a failure leaves the
	// mapping usable but unadvertised.
	if values, err := m.upnpAction(ctx, service, "GetExternalIPAddress", nil); err == nil {
		if raw := values["NewExternalIPAddress"]; raw != "" {
			mapping.ExternalIP = validateExternalIP(net.ParseIP(raw))
		}
	}
	return mapping, nil
}

func (m *Manager) unmapUPnP(ctx context.Context, mapping Mapping) error {
	service, err := m.upnpService(ctx)
	if err != nil {
		return err
	}
	_, err = m.upnpAction(ctx, service, "DeletePortMapping", []upnpArg{
		{Name: "NewRemoteHost", Value: ""},
		{Name: "NewExternalPort", Value: strconv.Itoa(mapping.ExternalPort)},
		{Name: "NewProtocol", Value: strings.ToUpper(string(mapping.Protocol))},
	})
	return err
}

func (m *Manager) upnpService(ctx context.Context) (*upnpService, error) {
	m.upnpMu.Lock()
	defer m.upnpMu.Unlock()
	if m.upnpSvc != nil {
		return m.upnpSvc, nil
	}
	var (
		service *upnpService
		err     error
	)
	if m.upnpLoc != "" {
		var parsed *url.URL
		parsed, err = url.Parse(m.upnpLoc)
		if err != nil {
			return nil, fmt.Errorf("upnp: invalid description URL: %w", err)
		}
		service, err = m.upnpFromDescription(ctx, parsed)
	} else {
		service, err = m.discoverUPnP(ctx)
	}
	if err != nil {
		return nil, err
	}
	m.upnpSvc = service
	return service, nil
}

func (m *Manager) discoverUPnP(ctx context.Context) (*upnpService, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		return nil, fmt.Errorf("upnp: discovery socket: %w", err)
	}
	defer conn.Close()
	target, err := net.ResolveUDPAddr("udp4", upnpDiscoveryAddress)
	if err != nil {
		return nil, err
	}
	message := "M-SEARCH * HTTP/1.1\r\n" +
		"HOST: " + upnpDiscoveryAddress + "\r\n" +
		"MAN: \"ssdp:discover\"\r\n" +
		"MX: 2\r\n" +
		"ST: " + upnpSearchTarget + "\r\n\r\n"
	if _, err := conn.WriteToUDP([]byte(message), target); err != nil {
		return nil, fmt.Errorf("upnp: discovery send: %w", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetReadDeadline(deadline)
	locations := make([]string, 0, 4)
	seen := map[string]bool{}
	buf := make([]byte, 2048)
	for len(locations) < 4 {
		n, source, err := conn.ReadFromUDP(buf)
		if err != nil {
			break
		}
		location, ok := ssdpLocation(buf[:n])
		if !ok || seen[location] {
			continue
		}
		parsed, err := url.Parse(location)
		if err != nil || parsed.Scheme != "http" || parsed.Host == "" {
			continue
		}
		// SSDP is unauthenticated. Only follow a description on the same host
		// that answered, and only for private or link-local gateways.
		sourceIP := source.IP
		host, _, err := net.SplitHostPort(parsed.Host)
		if err != nil {
			host = parsed.Host
		}
		if host != sourceIP.String() || !privateGatewayIP(sourceIP) {
			continue
		}
		seen[location] = true
		locations = append(locations, location)
	}
	var failures []error
	for _, location := range locations {
		parsed, _ := url.Parse(location)
		service, err := m.upnpFromDescription(ctx, parsed)
		if err == nil {
			return service, nil
		}
		failures = append(failures, err)
	}
	if len(failures) > 0 {
		return nil, errors.Join(failures...)
	}
	return nil, errors.New("upnp: no InternetGatewayDevice answered discovery")
}

func ssdpLocation(data []byte) (string, bool) {
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		name, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "LOCATION") {
			continue
		}
		value = strings.TrimSpace(value)
		if value != "" {
			return value, true
		}
	}
	return "", false
}

func privateGatewayIP(ip net.IP) bool {
	return ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLoopback()
}

func (m *Manager) upnpFromDescription(ctx context.Context, location *url.URL) (*upnpService, error) {
	client := &http.Client{
		Timeout: m.timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("upnp: redirects are not followed")
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upnp: fetch description: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upnp: description returned %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, upnpMaxDescription+1))
	if err != nil {
		return nil, err
	}
	if len(data) > upnpMaxDescription {
		return nil, errors.New("upnp: description is too large")
	}
	var description upnpDescription
	if err := xml.Unmarshal(data, &description); err != nil {
		return nil, fmt.Errorf("upnp: parse description: %w", err)
	}
	base := location
	if description.URLBase != "" {
		if parsed, err := url.Parse(description.URLBase); err == nil && parsed.IsAbs() {
			base = parsed
		}
	}
	service := findUPnPService(description.Device)
	if service == nil {
		return nil, errors.New("upnp: gateway has no WAN connection service")
	}
	control, err := base.Parse(service.ControlURL)
	if err != nil || control.Scheme != "http" || control.Host == "" {
		return nil, errors.New("upnp: invalid service control URL")
	}
	return &upnpService{serviceType: service.ServiceType, controlURL: control}, nil
}

func findUPnPService(device upnpDevice) *upnpServiceXML {
	for i := range device.Services {
		service := &device.Services[i]
		if strings.HasPrefix(service.ServiceType, "urn:schemas-upnp-org:service:WANIPConnection:") ||
			strings.HasPrefix(service.ServiceType, "urn:schemas-upnp-org:service:WANPPPConnection:") {
			if service.ControlURL != "" {
				return service
			}
		}
	}
	for i := range device.Devices {
		if found := findUPnPService(device.Devices[i]); found != nil {
			return found
		}
	}
	return nil
}

type upnpArg struct {
	Name  string
	Value string
}

func (m *Manager) upnpAction(ctx context.Context, service *upnpService, action string, args []upnpArg) (map[string]string, error) {
	var body bytes.Buffer
	body.WriteString(`<?xml version="1.0"?>`)
	body.WriteString(`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">`)
	body.WriteString(`<s:Body><u:` + action + ` xmlns:u="` + xmlEscape(service.serviceType) + `">`)
	for _, arg := range args {
		body.WriteString("<" + arg.Name + ">" + xmlEscape(arg.Value) + "</" + arg.Name + ">")
	}
	body.WriteString(`</u:` + action + `></s:Body></s:Envelope>`)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, service.controlURL.String(), &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPAction", `"`+service.serviceType+"#"+action+`"`)
	client := &http.Client{
		Timeout: m.timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("upnp: redirects are not followed")
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upnp: %s: %w", action, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, upnpMaxResponse+1))
	if err != nil {
		return nil, err
	}
	if len(data) > upnpMaxResponse {
		return nil, errors.New("upnp: action response is too large")
	}
	if resp.StatusCode != http.StatusOK {
		if code := upnpErrorCode(data); code != "" {
			return nil, fmt.Errorf("upnp: %s failed with error %s", action, code)
		}
		return nil, fmt.Errorf("upnp: %s returned %d", action, resp.StatusCode)
	}
	return upnpResponseValues(data), nil
}

type upnpActionResponse struct {
	XMLName xml.Name
	Values  []struct {
		XMLName xml.Name
		Value   string `xml:",chardata"`
	} `xml:",any"`
}

func upnpResponseValues(data []byte) map[string]string {
	var envelope struct {
		Body struct {
			Action upnpActionResponse `xml:",any"`
		} `xml:"Body"`
	}
	values := map[string]string{}
	if err := xml.Unmarshal(data, &envelope); err != nil {
		return values
	}
	for _, item := range envelope.Body.Action.Values {
		values[item.XMLName.Local] = strings.TrimSpace(item.Value)
	}
	return values
}

func upnpErrorCode(data []byte) string {
	var envelope struct {
		Body struct {
			Fault struct {
				Detail struct {
					UPnPError struct {
						Code string `xml:"errorCode"`
					} `xml:"UPnPError"`
				} `xml:"detail"`
			} `xml:"Fault"`
		} `xml:"Body"`
	}
	if err := xml.Unmarshal(data, &envelope); err != nil {
		return ""
	}
	return strings.TrimSpace(envelope.Body.Fault.Detail.UPnPError.Code)
}

func localIPForURL(target *url.URL) (net.IP, error) {
	host := target.Hostname()
	port := target.Port()
	if port == "" {
		port = "80"
	}
	address := net.JoinHostPort(host, port)
	var dialer net.Dialer
	conn, err := dialer.Dial("udp", address)
	if err != nil {
		return nil, fmt.Errorf("upnp: determine local address: %w", err)
	}
	defer conn.Close()
	local, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || local.IP == nil || local.IP.IsUnspecified() {
		return nil, errors.New("upnp: could not determine the local address")
	}
	return local.IP, nil
}

func xmlEscape(value string) string {
	var buffer bytes.Buffer
	_ = xml.EscapeText(&buffer, []byte(value))
	return buffer.String()
}
