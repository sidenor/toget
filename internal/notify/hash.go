package notify

import (
	"crypto/sha256"
	"hash"
)

func sha256New() hash.Hash { return sha256.New() }
