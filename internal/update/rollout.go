package update

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
)

// LocalRolloutID derives a stable, non-secret rollout identity for a node
// before it has a managed installation. It is deliberately local: the release
// service never learns which release a device was offered.
func LocalRolloutID(home string) string {
	host, _ := os.Hostname()
	sum := sha256.Sum256([]byte("toget-update-rollout-id:v1\n" + host + "\n" + home))
	return hex.EncodeToString(sum[:])
}
