package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sidenor/toget/internal/card"
	"github.com/sidenor/toget/internal/identity"
)

const maxCardLabelRunes = 128

var (
	// ErrCardNotFound means no saved card has the requested server ID.
	ErrCardNotFound = errors.New("saved card not found")
	// ErrInvalidCardLabel means a local card label is too long or contains
	// control characters.
	ErrInvalidCardLabel = errors.New("card label is invalid")
)

// CardView is the public shape of an own or saved business card.
type CardView struct {
	ServerID          string     `json:"server_id"`
	ServerCode        string     `json:"server_code"`
	AccountID         string     `json:"account_id"`
	Label             string     `json:"label,omitempty"`
	Card              string     `json:"card"`
	AddedAt           *time.Time `json:"added_at,omitempty"`
	IdentityExpiresAt time.Time  `json:"identity_expires_at"`
	NodeExpiresAt     time.Time  `json:"node_expires_at"`
}

type verifiedCard struct {
	entry    card.Entry
	identity *identity.Manifest
	node     *Manifest
}

// OwnCard builds this node's portable business card. The card embeds the
// one-year signed identity manifest and a fresh 24-hour signed node manifest.
// label defaults to the account name.
func (s *Service) OwnCard(ctx context.Context, label string) (*CardView, error) {
	if s == nil {
		return nil, errors.New("node transport is disabled")
	}
	snapshot, err := s.accounts.Get(ctx)
	if err != nil {
		return nil, err
	}
	if snapshot.Server == nil || snapshot.Account == nil {
		return nil, errors.New("the node identity is not ready")
	}
	manifest, err := s.manifest(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(label) == "" {
		// An account name with unusual characters should not prevent the
		// owner from exporting a card; fall back to no label in that case.
		if cleaned, cleanErr := cleanCardLabel(snapshot.Account.Name); cleanErr == nil {
			label = cleaned
		}
	}
	label, err = cleanCardLabel(label)
	if err != nil {
		return nil, err
	}
	nodeJSON, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	portable, err := card.New(label, json.RawMessage(snapshot.Server.Manifest), nodeJSON)
	if err != nil {
		return nil, err
	}
	encoded, err := portable.Encode()
	if err != nil {
		return nil, err
	}
	return &CardView{
		ServerID: snapshot.Server.ID, ServerCode: snapshot.Server.Code,
		AccountID: snapshot.Account.ID, Label: label, Card: encoded,
		IdentityExpiresAt: manifestTime(snapshot.Server.Manifest), NodeExpiresAt: manifest.ExpiresAt,
	}, nil
}

func manifestTime(raw string) time.Time {
	var manifest identity.Manifest
	if err := json.Unmarshal([]byte(raw), &manifest); err != nil {
		return time.Time{}
	}
	return manifest.ExpiresAt
}

// AddCard verifies and stores a portable card. Signatures are checked even if
// the embedded 24-hour node manifest has expired; the live node is verified
// again before the card can be used for a connection.
func (s *Service) AddCard(ctx context.Context, raw, label string) (*CardView, error) {
	if s == nil {
		return nil, errors.New("node transport is disabled")
	}
	if s.cards == nil {
		return nil, errors.New("card storage is unavailable")
	}
	portable, err := card.Decode(raw)
	if err != nil {
		return nil, err
	}
	verified, err := verifyCard(portable)
	if err != nil {
		return nil, err
	}
	if snapshot, getErr := s.accounts.Get(ctx); getErr == nil && snapshot.Server != nil &&
		snapshot.Server.ID == verified.identity.ServerID {
		return nil, errors.New("this is your own card")
	}
	if strings.TrimSpace(label) == "" {
		// A card's label is unsigned remote input. If it is unusable, fall
		// back to the server code instead of rejecting an otherwise valid card.
		if cleaned, cleanErr := cleanCardLabel(portable.Label); cleanErr == nil {
			label = cleaned
		}
	}
	if strings.TrimSpace(label) == "" {
		label = verified.identity.ServerCode
	}
	label, err = cleanCardLabel(label)
	if err != nil {
		return nil, err
	}
	encoded, err := portable.Encode()
	if err != nil {
		return nil, err
	}
	entry := card.Entry{
		ID: verified.identity.ServerID, Label: label,
		ServerCode: verified.identity.ServerCode, AccountID: verified.node.AccountID,
		Card: encoded, AddedAt: time.Now().UTC(),
	}
	if existing, ok := s.cards.Get(entry.ID); ok {
		entry.AddedAt = existing.AddedAt
	}
	if err := s.cards.Put(entry); err != nil {
		return nil, err
	}
	added := entry.AddedAt
	return &CardView{
		ServerID: entry.ID, ServerCode: entry.ServerCode, AccountID: entry.AccountID,
		Label: entry.Label, Card: entry.Card, AddedAt: &added,
		IdentityExpiresAt: verified.identity.ExpiresAt, NodeExpiresAt: verified.node.ExpiresAt,
	}, nil
}

// Cards lists saved cards. The embedded node manifest may have expired; it is
// refreshed by live verification at lookup time.
func (s *Service) Cards() []CardView {
	if s == nil || s.cards == nil {
		return nil
	}
	entries := s.cards.List()
	out := make([]CardView, 0, len(entries))
	for _, entry := range entries {
		portable, err := card.Decode(entry.Card)
		if err != nil {
			continue
		}
		verified, err := verifyCard(portable)
		if err != nil || verified.identity.ServerID != entry.ID ||
			verified.identity.ServerCode != entry.ServerCode {
			continue
		}
		added := entry.AddedAt
		out = append(out, CardView{
			ServerID: entry.ID, ServerCode: entry.ServerCode, AccountID: verified.node.AccountID,
			Label: entry.Label, Card: entry.Card, AddedAt: &added,
			IdentityExpiresAt: verified.identity.ExpiresAt, NodeExpiresAt: verified.node.ExpiresAt,
		})
	}
	return out
}

// RemoveCard deletes a saved card by server ID.
func (s *Service) RemoveCard(id string) error {
	if s == nil || s.cards == nil {
		return ErrCardNotFound
	}
	removed, err := s.cards.Remove(strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if !removed {
		return ErrCardNotFound
	}
	return nil
}

func verifyCard(portable *card.Card) (*verifiedCard, error) {
	if portable == nil {
		return nil, errors.New("card is missing")
	}
	im, err := identity.VerifyManifestJSON(portable.Identity)
	if err != nil {
		return nil, fmt.Errorf("card identity is invalid: %w", err)
	}
	serverKey, err := identity.DecodePublicKey(im.RootPubKey)
	if err != nil {
		return nil, err
	}
	var manifest Manifest
	if err := json.Unmarshal(portable.Node, &manifest); err != nil {
		return nil, errors.New("card node manifest is invalid")
	}
	if err := verifyManifestSignature(&manifest, serverKey); err != nil {
		return nil, fmt.Errorf("card node manifest is invalid: %w", err)
	}
	if manifest.ServerID != im.ServerID || manifest.ServerCode != im.ServerCode {
		return nil, errors.New("card node manifest does not match its server")
	}
	if !validAccountID(manifest.AccountID, im.ServerCode) {
		return nil, errors.New("card account ID is invalid")
	}
	parsed, err := url.Parse(manifest.Endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, errors.New("card endpoint is not a plain HTTPS address")
	}
	if _, err := ParseFingerprint(manifest.TLSFingerprint); err != nil {
		return nil, err
	}
	return &verifiedCard{identity: im, node: &manifest}, nil
}

func validAccountID(accountID, serverCode string) bool {
	if len(accountID) != 14 || !strings.HasPrefix(accountID, serverCode) {
		return false
	}
	for _, r := range accountID {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func cleanCardLabel(label string) (string, error) {
	label = strings.TrimSpace(label)
	if utf8.RuneCountInString(label) > maxCardLabelRunes {
		return "", fmt.Errorf("%w: too long", ErrInvalidCardLabel)
	}
	for _, r := range label {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: contains control characters", ErrInvalidCardLabel)
		}
	}
	return label, nil
}

// verifiedCardsForCode returns saved cards matching a server code. Every card
// is signature-checked; a stale or conflicting card fails closed instead of
// silently bypassing a pin.
func (s *Service) verifiedCardsForCode(code string) ([]verifiedCard, error) {
	if s == nil || s.cards == nil {
		return nil, nil
	}
	var out []verifiedCard
	found := false
	for _, entry := range s.cards.List() {
		if entry.ServerCode != code {
			continue
		}
		found = true
		portable, err := card.Decode(entry.Card)
		if err != nil {
			return nil, fmt.Errorf("saved card %s is invalid: %w", entry.ID, err)
		}
		verified, err := verifyCard(portable)
		if err != nil {
			return nil, fmt.Errorf("saved card %s is invalid: %w", entry.Label, err)
		}
		if verified.identity.ServerCode != code {
			return nil, errors.New("saved card does not match its server code")
		}
		verified.entry = entry
		out = append(out, *verified)
	}
	if !found {
		return nil, nil
	}
	ids := make(map[string]bool)
	for _, saved := range out {
		ids[saved.identity.ServerID] = true
	}
	if len(ids) > 1 {
		return nil, errors.New("multiple saved cards use this server code; remove one before looking it up")
	}
	return out, nil
}

func (s *Service) lookupSavedCards(ctx context.Context, cards []verifiedCard) (*DirectoryRecord, error) {
	if len(cards) == 0 {
		return nil, errors.New("no saved card")
	}
	var lastErr error
	for _, saved := range cards {
		record, err := s.verifyPinnedPeer(ctx, saved.node.Endpoint, saved.node.TLSFingerprint,
			saved.identity.ServerID, saved.identity.ServerCode, saved.entry.AddedAt, "card")
		if err == nil {
			return record, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("saved card for %s is unreachable: %w", cards[0].identity.ServerCode, lastErr)
}

func checkSavedCardPin(cards []verifiedCard, record *DirectoryRecord) error {
	if len(cards) == 0 || record == nil {
		return nil
	}
	if record.ServerID != cards[0].identity.ServerID {
		return errors.New("the resolved server does not match the saved card for this code; remove the card to allow a different server")
	}
	return nil
}
