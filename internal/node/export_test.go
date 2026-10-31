package node

import (
	"context"
	"crypto/ed25519"

	"github.com/sidenor/toget/internal/mdns"
)

// VerifyMDNSPeerForTest exposes the mDNS verification path to external tests
// without making it part of the package API.
func VerifyMDNSPeerForTest(service *Service, ctx context.Context, peer mdns.Peer) (*DirectoryRecord, error) {
	return service.verifyMDNSPeer(ctx, peer)
}

// SignNodeManifestForTest signs a node manifest with a server key.
func SignNodeManifestForTest(manifest *Manifest, key ed25519.PrivateKey) error {
	return signManifest(manifest, key)
}
