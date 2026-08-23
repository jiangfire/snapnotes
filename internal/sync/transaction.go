package sync

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/jiangfire/snapnotes/internal/protocol"
)

// Transaction is a fully prepared protocol transaction ready for transport.
type Transaction struct {
	protocol.UnsignedBody
	TransactionID []byte
	Signature     []byte
	PowEpoch      uint64
	PowNonce      uint64
}

// Encode returns the canonical CBOR encoding of the full transaction, suitable
// for durable outbox storage and exact idempotent resubmission.
func (t Transaction) Encode() ([]byte, error) {
	return protocol.MarshalTransaction(
		t.UnsignedBody, t.TransactionID, t.Signature, t.PowEpoch, t.PowNonce,
	)
}

// Hash returns the SHA-256 transaction hash over the canonical transaction.
func (t Transaction) Hash() ([]byte, error) {
	return protocol.TransactionHash(
		t.UnsignedBody, t.TransactionID, t.Signature, t.PowEpoch, t.PowNonce,
	)
}

// DecodeTransaction reverses Encode.
func DecodeTransaction(data []byte) (Transaction, error) {
	body, txID, sig, epoch, nonce, err := protocol.UnmarshalTransaction(data)
	if err != nil {
		return Transaction{}, err
	}
	return Transaction{
		UnsignedBody:  body,
		TransactionID: txID,
		Signature:     sig,
		PowEpoch:      epoch,
		PowNonce:      nonce,
	}, nil
}

// DecodeSignedTransaction decodes a canonical transaction CBOR blob into the
// protocol-level SignedTransaction carried by protocol.Block. It is used by the
// server to re-wrap stored transaction CBOR into a CBOR block for transport.
func DecodeSignedTransaction(data []byte) (protocol.SignedTransaction, error) {
	tx, err := DecodeTransaction(data)
	if err != nil {
		return protocol.SignedTransaction{}, err
	}
	return toProtocolSignedTransaction(tx), nil
}

// b64url is a []byte that JSON-encodes as unpadded base64url, per the wire spec.
type b64url []byte

func (b b64url) MarshalJSON() ([]byte, error) {
	return json.Marshal(base64.RawURLEncoding.EncodeToString(b))
}

func (b *b64url) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	decoded, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return err
	}
	*b = decoded
	return nil
}

// WireTransaction is the JSON transport representation of a transaction.
// Binary fields use unpadded base64url; hashes and signatures are derived by
// the receiver and never trusted from the wire.
type WireTransaction struct {
	ProtocolVersion  uint64 `json:"protocol_version"`
	StreamID         b64url `json:"stream_id"`
	NoteID           b64url `json:"note_id"`
	OperationType    string `json:"operation_type"`
	OperationPayload b64url `json:"operation_payload"`
	ClientCreatedAt  uint64 `json:"client_created_at"`
	AuthorPublicKey  b64url `json:"author_public_key"`
	TransactionID    b64url `json:"transaction_id"`
	Signature        b64url `json:"signature"`
	PowEpoch         uint64 `json:"pow_epoch"`
	PowNonce         uint64 `json:"pow_nonce"`
}

// ToWire converts a Transaction into its JSON transport form.
func (t Transaction) ToWire() WireTransaction {
	return WireTransaction{
		ProtocolVersion:  t.ProtocolVersion,
		StreamID:         append(b64url(nil), t.StreamID...),
		NoteID:           append(b64url(nil), t.NoteID...),
		OperationType:    t.OperationType,
		OperationPayload: append(b64url(nil), t.OperationPayload...),
		ClientCreatedAt:  t.ClientCreatedAt,
		AuthorPublicKey:  append(b64url(nil), t.AuthorPublicKey...),
		TransactionID:    append(b64url(nil), t.TransactionID...),
		Signature:        append(b64url(nil), t.Signature...),
		PowEpoch:         t.PowEpoch,
		PowNonce:         t.PowNonce,
	}
}

// FromWire reconstructs a Transaction from its JSON transport form.
func (w WireTransaction) FromWire() Transaction {
	return Transaction{
		UnsignedBody: protocol.UnsignedBody{
			ProtocolVersion:  w.ProtocolVersion,
			StreamID:         append([]byte(nil), w.StreamID...),
			NoteID:           append([]byte(nil), w.NoteID...),
			OperationType:    w.OperationType,
			OperationPayload: append([]byte(nil), w.OperationPayload...),
			ClientCreatedAt:  w.ClientCreatedAt,
			AuthorPublicKey:  append([]byte(nil), w.AuthorPublicKey...),
		},
		TransactionID: append([]byte(nil), w.TransactionID...),
		Signature:     append([]byte(nil), w.Signature...),
		PowEpoch:      w.PowEpoch,
		PowNonce:      w.PowNonce,
	}
}

// MarshalWireJSON returns the JSON bytes of the wire transaction.
func (t Transaction) MarshalWireJSON() ([]byte, error) {
	return json.Marshal(t.ToWire())
}

// UnmarshalWireJSON parses a JSON transport transaction. It rejects unknown
// fields at the trust boundary so a peer cannot smuggle ignored-but-trusted
// fields past validation (M1).
func UnmarshalWireJSON(data []byte) (Transaction, error) {
	var w WireTransaction
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return Transaction{}, err
	}
	if len(w.StreamID) != 32 || len(w.NoteID) != 32 || len(w.AuthorPublicKey) != 32 {
		return Transaction{}, errors.New("wire transaction IDs must be 32 bytes")
	}
	return w.FromWire(), nil
}
