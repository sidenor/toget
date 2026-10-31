package node

import (
	"testing"

	"github.com/sidenor/toget/internal/identity"
)

func TestCheckSavedCardPinRejectsDifferentServer(t *testing.T) {
	cards := []verifiedCard{{
		identity: &identity.Manifest{ServerID: "tog1:s:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", ServerCode: "12345678"},
	}}
	if err := checkSavedCardPin(cards, &DirectoryRecord{
		ServerID: cards[0].identity.ServerID, ServerCode: "12345678",
	}); err != nil {
		t.Fatalf("matching server was rejected: %v", err)
	}
	if err := checkSavedCardPin(cards, &DirectoryRecord{
		ServerID: "tog1:s:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", ServerCode: "12345678",
	}); err == nil {
		t.Fatal("a different server with the same code bypassed the saved card pin")
	}
	if err := checkSavedCardPin(nil, &DirectoryRecord{ServerID: "tog1:s:B"}); err != nil {
		t.Fatalf("lookup without a saved card should not be pinned: %v", err)
	}
}
