package mdns

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	// ServiceName is the DNS-SD service browsed by Toget nodes.
	ServiceName = "_toget._tcp.local."
	// TTL is the advertised record lifetime. Records are refreshed every
	// TTL/2 while a node is running.
	TTL = 120
	// MaxPacket bounds one mDNS packet. RFC 6762 recommends 9000 bytes for
	// multicast DNS; Toget's TXT records are far smaller.
	MaxPacket = 9000

	txtFormatVersion = "1"
	cacheFlush       = 0x8000
)

// Info is the self-reported advertisement of one node. It is deliberately
// small: the identity manifest is fetched over pinned TLS after discovery,
// not trusted from the multicast packet.
type Info struct {
	ID          string
	Code        string
	Protocol    int
	Fingerprint string
	Port        int
	// Addresses optionally overrides the interface addresses used for A/AAAA
	// records. When empty, every unicast address on the sending interface is
	// advertised.
	Addresses []net.IP
}

// Peer is one node learned from multicast DNS. The fields are advertised,
// not authenticated. Callers that need trust should fetch and verify the
// node's signed identity and node manifests from the advertised address.
type Peer struct {
	ServerID       string    `json:"server_id"`
	ServerCode     string    `json:"server_code"`
	Protocol       int       `json:"protocol"`
	TLSFingerprint string    `json:"tls_fingerprint"`
	Port           int       `json:"port"`
	Addresses      []string  `json:"addresses"`
	LastSeen       time.Time `json:"last_seen"`
}

func validateInfo(info Info) error {
	if !strings.HasPrefix(info.ID, "tog1:s:") || len(info.ID) > 160 {
		return errors.New("mdns: invalid server ID")
	}
	if len(info.Code) != 8 {
		return errors.New("mdns: server code must be 8 digits")
	}
	for _, digit := range info.Code {
		if digit < '0' || digit > '9' {
			return errors.New("mdns: server code must be 8 digits")
		}
	}
	if info.Protocol < 1 {
		return errors.New("mdns: protocol must be positive")
	}
	if _, err := normalizeFingerprint(info.Fingerprint); err != nil {
		return err
	}
	if info.Port < 1 || info.Port > 65535 {
		return errors.New("mdns: port is out of range")
	}
	for _, address := range info.Addresses {
		if address == nil || address.IsUnspecified() || address.IsMulticast() {
			return errors.New("mdns: advertised address is not unicast")
		}
	}
	return nil
}

func normalizeFingerprint(raw string) (string, error) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) != sha256.Size {
		return "", errors.New("mdns: invalid TLS fingerprint")
	}
	return hex.EncodeToString(decoded), nil
}

// label returns a stable DNS label for this node. The full server ID stays in
// TXT so DNS escaping and the 63-byte label limit never apply to it.
func label(serverID string) string {
	sum := sha256.Sum256([]byte(serverID))
	return "toget-" + hex.EncodeToString(sum[:10])
}

func serviceName() dnsmessage.Name {
	return dnsmessage.MustNewName(ServiceName)
}

func instanceName(info Info) dnsmessage.Name {
	return dnsmessage.MustNewName(label(info.ID) + "." + ServiceName)
}

func hostName(info Info) dnsmessage.Name {
	return dnsmessage.MustNewName(label(info.ID) + ".local.")
}

func txtStrings(info Info) []string {
	fingerprint, _ := normalizeFingerprint(info.Fingerprint)
	return []string{
		"txtv=" + txtFormatVersion,
		"id=" + info.ID,
		"code=" + info.Code,
		"fp=" + fingerprint,
		"proto=" + strconv.Itoa(info.Protocol),
	}
}

func queryPacket() ([]byte, error) {
	builder := dnsmessage.NewBuilder(make([]byte, 0, 512), dnsmessage.Header{})
	builder.EnableCompression()
	if err := builder.StartQuestions(); err != nil {
		return nil, err
	}
	if err := builder.Question(dnsmessage.Question{
		Name: serviceName(), Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET,
	}); err != nil {
		return nil, err
	}
	return builder.Finish()
}

func advertisementPacket(info Info, addresses []net.IP, ttl uint32) ([]byte, error) {
	builder := dnsmessage.NewBuilder(make([]byte, 0, 1024), dnsmessage.Header{
		Response: true, Authoritative: true,
	})
	builder.EnableCompression()
	if err := builder.StartAnswers(); err != nil {
		return nil, err
	}
	service := serviceName()
	instance := instanceName(info)
	host := hostName(info)
	if err := builder.PTRResource(
		dnsmessage.ResourceHeader{Name: service, Class: dnsmessage.ClassINET, TTL: ttl},
		dnsmessage.PTRResource{PTR: instance},
	); err != nil {
		return nil, err
	}
	if err := builder.SRVResource(
		dnsmessage.ResourceHeader{
			Name: instance, Class: dnsmessage.Class(cacheFlush | uint16(dnsmessage.ClassINET)), TTL: ttl,
		},
		dnsmessage.SRVResource{Priority: 0, Weight: 0, Port: uint16(info.Port), Target: host},
	); err != nil {
		return nil, err
	}
	if err := builder.TXTResource(
		dnsmessage.ResourceHeader{
			Name: instance, Class: dnsmessage.Class(cacheFlush | uint16(dnsmessage.ClassINET)), TTL: ttl,
		},
		dnsmessage.TXTResource{TXT: txtStrings(info)},
	); err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return builder.Finish()
	}
	if err := builder.StartAdditionals(); err != nil {
		return nil, err
	}
	for _, address := range addresses {
		if address == nil || address.IsUnspecified() || address.IsMulticast() {
			continue
		}
		header := dnsmessage.ResourceHeader{
			Name: host, Class: dnsmessage.Class(cacheFlush | uint16(dnsmessage.ClassINET)), TTL: ttl,
		}
		if v4 := address.To4(); v4 != nil {
			var record dnsmessage.AResource
			copy(record.A[:], v4)
			if err := builder.AResource(header, record); err != nil {
				return nil, err
			}
			continue
		}
		if v6 := address.To16(); v6 != nil {
			var record dnsmessage.AAAAResource
			copy(record.AAAA[:], v6)
			if err := builder.AAAAResource(header, record); err != nil {
				return nil, err
			}
		}
	}
	return builder.Finish()
}

// questions returns the questions of a non-response packet.
func questions(packet []byte) ([]dnsmessage.Question, error) {
	var parser dnsmessage.Parser
	header, err := parser.Start(packet)
	if err != nil {
		return nil, err
	}
	if header.Response {
		return nil, nil
	}
	return parser.AllQuestions()
}

func wantsName(questions []dnsmessage.Question, info Info) bool {
	service := serviceName().String()
	instance := instanceName(info).String()
	host := hostName(info).String()
	for _, question := range questions {
		name := question.Name.String()
		switch {
		case strings.EqualFold(name, service):
			if question.Type == dnsmessage.TypePTR || question.Type == dnsmessage.TypeALL {
				return true
			}
		case strings.EqualFold(name, instance):
			if question.Type == dnsmessage.TypeSRV || question.Type == dnsmessage.TypeTXT ||
				question.Type == dnsmessage.TypeALL {
				return true
			}
		case strings.EqualFold(name, host):
			if question.Type == dnsmessage.TypeA || question.Type == dnsmessage.TypeAAAA ||
				question.Type == dnsmessage.TypeALL {
				return true
			}
		}
	}
	return false
}

// observation is one resource record from a response. The service cache
// combines observations from several packets because responders are allowed
// to split PTR, SRV, TXT, and address records across messages.
type observation struct {
	kind    string
	name    string
	target  string
	port    int
	text    string
	address net.IP
	ttl     uint32
}

func observations(packet []byte) (bool, []observation, error) {
	var parser dnsmessage.Parser
	header, err := parser.Start(packet)
	if err != nil {
		return false, nil, err
	}
	if !header.Response {
		return false, nil, nil
	}
	if err := parser.SkipAllQuestions(); err != nil {
		return true, nil, err
	}
	var records []dnsmessage.Resource
	for _, section := range []func() ([]dnsmessage.Resource, error){
		parser.AllAnswers, parser.AllAuthorities, parser.AllAdditionals,
	} {
		part, err := section()
		if err != nil {
			return true, nil, err
		}
		records = append(records, part...)
	}
	out := make([]observation, 0, len(records))
	for _, record := range records {
		header := record.Header
		if uint16(header.Class)&^uint16(cacheFlush) != uint16(dnsmessage.ClassINET) {
			continue
		}
		ttl := header.TTL
		name := header.Name.String()
		switch body := record.Body.(type) {
		case *dnsmessage.PTRResource:
			if strings.EqualFold(name, ServiceName) {
				out = append(out, observation{kind: "ptr", name: name, target: body.PTR.String(), ttl: ttl})
			}
		case *dnsmessage.SRVResource:
			out = append(out, observation{
				kind: "srv", name: name, target: body.Target.String(),
				port: int(body.Port), ttl: ttl,
			})
		case *dnsmessage.TXTResource:
			for _, value := range body.TXT {
				out = append(out, observation{kind: "txt", name: name, text: value, ttl: ttl})
			}
		case *dnsmessage.AResource:
			out = append(out, observation{
				kind: "a", name: name, address: net.IPv4(body.A[0], body.A[1], body.A[2], body.A[3]),
				ttl: ttl,
			})
		case *dnsmessage.AAAAResource:
			out = append(out, observation{
				kind: "aaaa", name: name, address: net.IP(append([]byte(nil), body.AAAA[:]...)),
				ttl: ttl,
			})
		}
	}
	return true, out, nil
}

func sortedIPs(ips []net.IP) []net.IP {
	unique := make(map[string]net.IP, len(ips))
	for _, ip := range ips {
		if ip == nil || ip.IsUnspecified() || ip.IsMulticast() {
			continue
		}
		unique[ip.String()] = ip
	}
	out := make([]net.IP, 0, len(unique))
	for _, ip := range unique {
		out = append(out, ip)
	}
	sort.Slice(out, func(i, j int) bool {
		// IPv4 first makes the common LAN address the first candidate for a
		// caller that only tries one address.
		i4, j4 := out[i].To4() != nil, out[j].To4() != nil
		if i4 != j4 {
			return i4
		}
		return out[i].String() < out[j].String()
	})
	return out
}

func familyAddresses(addresses []net.IP, network string) []net.IP {
	out := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		if network == "udp4" && address.To4() == nil {
			continue
		}
		if network == "udp6" && address.To4() != nil {
			continue
		}
		out = append(out, address)
	}
	return sortedIPs(out)
}

func parseTXT(text string) (key, value string, ok bool) {
	key, value, ok = strings.Cut(text, "=")
	if !ok || key == "" {
		return "", "", false
	}
	return key, value, true
}

func validInstance(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, "."+strings.ToLower(ServiceName)) &&
		len(name) > len(ServiceName)+2
}
