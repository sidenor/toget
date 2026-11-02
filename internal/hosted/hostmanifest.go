//go:build !js

package hosted

import (
	"time"

	"github.com/sidenor/toget/internal/identity"
)

// SignHostManifest produces a short-lived discovery statement for this account
// host. It is separate from the long-lived identity manifest so a directory
// can expire or update the public endpoint without changing the host identity.
func (s *Service) SignHostManifest(rawURL, rawFingerprint string, ttl time.Duration) ([]byte, error) {
	return identity.SignHostManifest(s.host.Server, s.host.Name, rawURL, rawFingerprint, s.now().UTC(), ttl)
}
