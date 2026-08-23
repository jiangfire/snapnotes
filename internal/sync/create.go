package sync

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"time"

	"github.com/jiangfire/snapnotes/internal/parser"
	"github.com/jiangfire/snapnotes/internal/protocol"
)

const maxPowAttempts = 1 << 28

// LooseTestTarget returns a permissive 256-bit PoW target (2^240) used by tests
// and the single-node MVP. The reference-device 100ms genesis target must be
// recorded before a production stream is created (see protocol-v1.md).
func LooseTestTarget() []byte {
	target := make([]byte, 32)
	target[1] = 0x01 // 2^240
	return target
}

// payloadContent is the canonical CBOR plaintext sealed into encrypted_payload.
type payloadContent struct {
	Text       string          `cbor:"text"`
	Tags       []string        `cbor:"tags,omitempty"`
	NoteDate   string          `cbor:"note_date,omitempty"`
	Reminder   *string         `cbor:"reminder,omitempty"`
	CheckItems []checkItemWire `cbor:"check_items"`
}

type checkItemWire struct {
	Text    string `cbor:"text"`
	Checked bool   `cbor:"checked"`
}

// createOperationPayload is the canonical CBOR operation_payload for a create.
type createOperationPayload struct {
	KeyEpoch         uint64 `cbor:"key_epoch"`
	EncryptedPayload []byte `cbor:"encrypted_payload"`
	PayloadNonce     []byte `cbor:"payload_nonce"`
	WrappedDEK       []byte `cbor:"wrapped_dek"`
	WrappedDEKNonce  []byte `cbor:"wrapped_dek_nonce"`
}

// CreateParams configures BuildCreate.
type CreateParams struct {
	StreamID         []byte
	StreamKey        []byte // key_epoch key used to wrap the DEK
	KeyEpoch         uint64
	AuthorPublicKey  ed25519.PublicKey
	SigningKey       ed25519.PrivateKey
	Body             string
	ClientCreatedAt  time.Time
	Randomness       io.Reader
	PowTarget        []byte // 32-byte big-endian PoW target
	PowEpoch         uint64
}

// BuildCreate produces a signed, PoW-stamped create transaction. The
// encrypted_payload and wrapped_dek envelopes bind protocol_version, stream_id,
// note_id, key_epoch, and field in their AAD; the authoritative transaction_id
// binding is provided by the Ed25519 signature over the unsigned body. (The
// transaction_id cannot be included in those envelopes because it is itself
// derived from the body that contains the ciphertext — a known circular
// dependency flagged for the Phase 3 review gate.)
func BuildCreate(p CreateParams) (Transaction, error) {
	if len(p.StreamID) != 32 {
		return Transaction{}, errors.New("stream_id must be 32 bytes")
	}
	if len(p.StreamKey) != 32 {
		return Transaction{}, errors.New("stream key must be 32 bytes")
	}
	if len(p.AuthorPublicKey) != ed25519.PublicKeySize {
		return Transaction{}, errors.New("author public key must be 32 bytes")
	}
	if len(p.SigningKey) != ed25519.PrivateKeySize {
		return Transaction{}, errors.New("signing key must be 64 bytes")
	}
	if p.Randomness == nil {
		p.Randomness = rand.Reader
	}
	if len(p.PowTarget) != 32 {
		return Transaction{}, errors.New("pow target must be 32 bytes")
	}

	noteID := make([]byte, 32)
	if _, err := io.ReadFull(p.Randomness, noteID); err != nil {
		return Transaction{}, err
	}

	parsed := parser.Parse(p.Body)
	content := payloadContent{Text: p.Body, Tags: parsed.Tags}
	if parsed.Date != "" {
		content.NoteDate = parsed.Date
	}
	if parsed.Reminder != nil {
		rfc := parsed.Reminder.UTC().Format(time.RFC3339)
		content.Reminder = &rfc
	}
	content.CheckItems = make([]checkItemWire, 0, len(parsed.CheckItems))
	for _, item := range parsed.CheckItems {
		content.CheckItems = append(content.CheckItems, checkItemWire{Text: item.Text, Checked: item.Checked})
	}

	plaintext, err := protocol.CanonicalMarshal(content)
	if err != nil {
		return Transaction{}, err
	}

	dek := make([]byte, 32)
	if _, err := io.ReadFull(p.Randomness, dek); err != nil {
		return Transaction{}, err
	}

	payloadAAD := protocol.EnvelopeAAD{
		ProtocolVersion: 1,
		StreamID:        p.StreamID,
		NoteID:          noteID,
		TransactionID:   make([]byte, 32), // placeholder; see BuildCreate doc
		KeyEpoch:        p.KeyEpoch,
		Field:           "encrypted_payload",
	}
	payloadNonce, encryptedPayload, err := protocol.SealPayloadRandom(dek, payloadAAD, plaintext, p.Randomness)
	if err != nil {
		return Transaction{}, err
	}

	dekAAD := protocol.EnvelopeAAD{
		ProtocolVersion: 1,
		StreamID:        p.StreamID,
		NoteID:          noteID,
		TransactionID:   make([]byte, 32),
		KeyEpoch:        p.KeyEpoch,
		Field:           "wrapped_dek",
	}
	dekNonce, wrappedDEK, err := protocol.SealPayloadRandom(p.StreamKey, dekAAD, dek, p.Randomness)
	if err != nil {
		return Transaction{}, err
	}

	opPayload, err := protocol.CanonicalMarshal(createOperationPayload{
		KeyEpoch:         p.KeyEpoch,
		EncryptedPayload: encryptedPayload,
		PayloadNonce:     payloadNonce,
		WrappedDEK:       wrappedDEK,
		WrappedDEKNonce:  dekNonce,
	})
	if err != nil {
		return Transaction{}, err
	}

	body := protocol.UnsignedBody{
		ProtocolVersion:  1,
		StreamID:         p.StreamID,
		NoteID:           noteID,
		OperationType:    "create",
		OperationPayload: opPayload,
		ClientCreatedAt:  uint64(p.ClientCreatedAt.UTC().UnixMilli()),
		AuthorPublicKey:  p.AuthorPublicKey,
	}

	txID, err := protocol.TransactionID(body)
	if err != nil {
		return Transaction{}, err
	}
	signature, err := protocol.SignUnsignedBody(body, p.SigningKey)
	if err != nil {
		return Transaction{}, err
	}
	powNonce, err := minePoW(body, txID, p.PowEpoch, p.PowTarget)
	if err != nil {
		return Transaction{}, err
	}

	return Transaction{
		UnsignedBody:  body,
		TransactionID: txID,
		Signature:     signature,
		PowEpoch:      p.PowEpoch,
		PowNonce:      powNonce,
	}, nil
}

func minePoW(body protocol.UnsignedBody, txID []byte, epoch uint64, target []byte) (uint64, error) {
	var nonce uint64
	for nonce < maxPowAttempts {
		preimage, err := protocol.PowPreimage(body, txID, epoch, nonce)
		if err != nil {
			return 0, err
		}
		if protocol.CheckPoW(preimage, target) {
			return nonce, nil
		}
		nonce++
	}
	return 0, errors.New("pow mining exceeded attempt budget")
}
