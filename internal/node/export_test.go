package node

import (
	"context"

	"github.com/sidenor/toget/internal/mdns"
)

// VerifyMDNSPeerForTest exposes the mDNS verification path to external tests
// without making it part of the package API.
func VerifyMDNSPeerForTest(service *Service, ctx context.Context, peer mdns.Peer) (*DirectoryRecord, error) {
	return service.verifyMDNSPeer(ctx, peer)
}
