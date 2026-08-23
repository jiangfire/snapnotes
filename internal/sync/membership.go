package sync

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"time"

	"github.com/jiangfire/snapnotes/internal/protocol"
)

func zero32() []byte { return make([]byte, 32) }

// signAndMine assembles an authorised, PoW-stamped transaction from an unsigned
// body. It is shared by every operation builder (create, member_add, key_grant,
// key_rotation_bundle) so the signing/PoW path stays identical across op types.
func signAndMine(body protocol.UnsignedBody, signingKey ed25519.PrivateKey, powEpoch uint64, powTarget []byte, randomness io.Reader) (Transaction, error) {
	if len(powTarget) != 32 {
		return Transaction{}, errors.New("pow target must be 32 bytes")
	}
	if randomness == nil {
		randomness = rand.Reader
	}
	txID, err := protocol.TransactionID(body)
	if err != nil {
		return Transaction{}, err
	}
	sig, err := protocol.SignUnsignedBody(body, signingKey)
	if err != nil {
		return Transaction{}, err
	}
	powNonce, err := minePoW(body, txID, powEpoch, powTarget)
	if err != nil {
		return Transaction{}, err
	}
	return Transaction{
		UnsignedBody:  body,
		TransactionID: txID,
		Signature:     sig,
		PowEpoch:      powEpoch,
		PowNonce:      powNonce,
	}, nil
}

func bodyFor(streamID []byte, author ed25519.PublicKey, opType string, opPayload []byte, createdAt time.Time) protocol.UnsignedBody {
	return protocol.UnsignedBody{
		ProtocolVersion:   1,
		StreamID:          append([]byte(nil), streamID...),
		NoteID:            zero32(), // mandatory zero note_id for membership/key ops
		OperationType:     opType,
		OperationPayload:  opPayload,
		ClientCreatedAt:   uint64(createdAt.UTC().UnixMilli()),
		AuthorPublicKey:   append([]byte(nil), author...),
	}
}

// MemberAddParams configures BuildMemberAdd.
type MemberAddParams struct {
	StreamID                  []byte
	OwnerPublicKey            ed25519.PublicKey
	OwnerSigningKey           ed25519.PrivateKey
	DeviceID                  []byte
	Label                     string
	MemberSigningPublicKey    ed25519.PublicKey
	MemberEncryptionPublicKey *ecdh.PublicKey
	PowTarget                 []byte
	PowEpoch                  uint64
	Randomness                io.Reader
}

// BuildMemberAdd produces a signed member_add transaction authorising a new
// device. Only the stream owner may issue it.
func BuildMemberAdd(p MemberAddParams) (Transaction, error) {
	if p.MemberEncryptionPublicKey == nil {
		return Transaction{}, errors.New("member encryption public key is required")
	}
	payload, err := protocol.CanonicalMarshal(protocol.MemberAddPayload{
		DeviceID:            append([]byte(nil), p.DeviceID...),
		Label:               p.Label,
		SigningPublicKey:    append([]byte(nil), p.MemberSigningPublicKey...),
		EncryptionPublicKey: p.MemberEncryptionPublicKey.Bytes(),
	})
	if err != nil {
		return Transaction{}, err
	}
	body := bodyFor(p.StreamID, p.OwnerPublicKey, "member_add", payload, time.Now())
	return signAndMine(body, p.OwnerSigningKey, p.PowEpoch, p.PowTarget, p.Randomness)
}

// GrantSpec describes one Stream Key grant inside key_grant or a rotation bundle.
type GrantSpec struct {
	RecipientDeviceID         []byte
	RecipientEncryptionPublicKey *ecdh.PublicKey
	KeyEpoch                  uint64
	StreamKey                 []byte
}

// KeyGrantParams configures BuildKeyGrant.
type KeyGrantParams struct {
	StreamID                  []byte
	OwnerPublicKey            ed25519.PublicKey
	OwnerSigningKey           ed25519.PrivateKey
	Grant                     GrantSpec
	PowTarget                 []byte
	PowEpoch                  uint64
	Randomness                io.Reader
}

// BuildKeyGrant wraps the grant's Stream Key to the recipient's encryption public
// key and produces a signed key_grant transaction. Only the owner may issue it.
func BuildKeyGrant(p KeyGrantParams) (Transaction, error) {
	if p.Grant.RecipientEncryptionPublicKey == nil {
		return Transaction{}, errors.New("recipient encryption public key is required")
	}
	envelope, err := protocol.EncryptKeyEnvelope(
		p.Grant.RecipientEncryptionPublicKey,
		protocol.KeyGrantAAD(p.StreamID, p.Grant.KeyEpoch),
		p.Grant.StreamKey,
		p.Randomness,
	)
	if err != nil {
		return Transaction{}, err
	}
	payload, err := protocol.CanonicalMarshal(protocol.KeyGrantPayload{
		RecipientDeviceID:         append([]byte(nil), p.Grant.RecipientDeviceID...),
		RecipientEncryptionPublicKey: p.Grant.RecipientEncryptionPublicKey.Bytes(),
		KeyEpoch:                  p.Grant.KeyEpoch,
		KeyEnvelope:               envelope,
	})
	if err != nil {
		return Transaction{}, err
	}
	body := bodyFor(p.StreamID, p.OwnerPublicKey, "key_grant", payload, time.Now())
	return signAndMine(body, p.OwnerSigningKey, p.PowEpoch, p.PowTarget, p.Randomness)
}

// KeyRotationBundleParams configures BuildKeyRotationBundle.
type KeyRotationBundleParams struct {
	StreamID              []byte
	OwnerPublicKey        ed25519.PublicKey
	OwnerSigningKey       ed25519.PrivateKey
	RevokedSigningKey     ed25519.PublicKey
	NewKeyEpoch           uint64
	Grants                []GrantSpec
	PowTarget             []byte
	PowEpoch              uint64
	Randomness            io.Reader
}

// BuildKeyRotationBundle produces the single atomic key_rotation_bundle
// transaction: revoke a signing key, advance the key epoch, and re-grant the new
// Stream Key to the surviving devices. There is no interleaving write window
// because the whole bundle is one transaction applied atomically by the server.
func BuildKeyRotationBundle(p KeyRotationBundleParams) (Transaction, error) {
	grants := make([]protocol.KeyRotationGrant, 0, len(p.Grants))
	for _, g := range p.Grants {
		if g.RecipientEncryptionPublicKey == nil {
			return Transaction{}, errors.New("grant recipient encryption public key is required")
		}
		envelope, err := protocol.EncryptKeyEnvelope(
			g.RecipientEncryptionPublicKey,
			protocol.KeyGrantAAD(p.StreamID, g.KeyEpoch),
			g.StreamKey,
			p.Randomness,
		)
		if err != nil {
			return Transaction{}, err
		}
		grants = append(grants, protocol.KeyRotationGrant{
			RecipientDeviceID:         append([]byte(nil), g.RecipientDeviceID...),
			RecipientEncryptionPublicKey: g.RecipientEncryptionPublicKey.Bytes(),
			KeyEpoch:                  g.KeyEpoch,
			KeyEnvelope:               envelope,
		})
	}
	payload, err := protocol.CanonicalMarshal(protocol.KeyRotationBundlePayload{
		RevokedSigningPublicKey: append([]byte(nil), p.RevokedSigningKey...),
		NewKeyEpoch:             p.NewKeyEpoch,
		Grants:                  grants,
	})
	if err != nil {
		return Transaction{}, err
	}
	body := bodyFor(p.StreamID, p.OwnerPublicKey, "key_rotation_bundle", payload, time.Now())
	return signAndMine(body, p.OwnerSigningKey, p.PowEpoch, p.PowTarget, p.Randomness)
}

// AcceptJoinParams configures AcceptJoin.
type AcceptJoinParams struct {
	StreamID        []byte
	OwnerPublicKey  ed25519.PublicKey
	OwnerSigningKey ed25519.PrivateKey
	CurrentEpoch    uint64
	StreamKey       []byte // current epoch Stream Key granted to the new device
	PowTarget       []byte
	PowEpoch        uint64
	Randomness      io.Reader
}

// AcceptJoin verifies a signed out-of-band join request against the trust anchor
// and, if valid, produces the member_add and key_grant transactions that bring the
// new device into the stream. The caller submits both to the server; the new
// device recovers the Stream Key by decrypting the key_grant envelope on sync.
func AcceptJoin(p AcceptJoinParams, joinReq protocol.JoinRequest, joinSig []byte, anchor protocol.JoinAnchor) (memberAdd, keyGrant Transaction, err error) {
	if err := protocol.VerifyJoinRequest(joinReq, joinSig, anchor); err != nil {
		return Transaction{}, Transaction{}, err
	}
	recipientEnc, eerr := ecdh.X25519().NewPublicKey(joinReq.EncryptionPublicKey)
	if eerr != nil {
		return Transaction{}, Transaction{}, eerr
	}
	memberAdd, err = BuildMemberAdd(MemberAddParams{
		StreamID:                  p.StreamID,
		OwnerPublicKey:            p.OwnerPublicKey,
		OwnerSigningKey:           p.OwnerSigningKey,
		DeviceID:                  joinReq.DeviceID,
		Label:                     joinReq.Label,
		MemberSigningPublicKey:    ed25519.PublicKey(joinReq.SigningPublicKey),
		MemberEncryptionPublicKey: recipientEnc,
		PowTarget:                 p.PowTarget,
		PowEpoch:                  p.PowEpoch,
		Randomness:                p.Randomness,
	})
	if err != nil {
		return Transaction{}, Transaction{}, err
	}
	keyGrant, err = BuildKeyGrant(KeyGrantParams{
		StreamID:        p.StreamID,
		OwnerPublicKey:  p.OwnerPublicKey,
		OwnerSigningKey: p.OwnerSigningKey,
		Grant: GrantSpec{
			RecipientDeviceID:         joinReq.DeviceID,
			RecipientEncryptionPublicKey: recipientEnc,
			KeyEpoch:                  p.CurrentEpoch,
			StreamKey:                 p.StreamKey,
		},
		PowTarget: p.PowTarget,
		PowEpoch:  p.PowEpoch,
		Randomness: p.Randomness,
	})
	if err != nil {
		return Transaction{}, Transaction{}, err
	}
	return memberAdd, keyGrant, nil
}
