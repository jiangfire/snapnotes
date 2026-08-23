package protocol

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

const protocolVersion uint64 = 1

type UnsignedBody struct {
	ProtocolVersion  uint64 `cbor:"protocol_version"`
	StreamID         []byte `cbor:"stream_id"`
	NoteID           []byte `cbor:"note_id"`
	OperationType    string `cbor:"operation_type"`
	OperationPayload []byte `cbor:"operation_payload"`
	ClientCreatedAt  uint64 `cbor:"client_created_at"`
	AuthorPublicKey  []byte `cbor:"author_public_key"`
}

type transactionFields struct {
	UnsignedBody  UnsignedBody `cbor:"unsigned_body"`
	TransactionID []byte       `cbor:"transaction_id"`
	Signature     []byte       `cbor:"signature"`
	PowEpoch      uint64       `cbor:"pow_epoch"`
	PowNonce      uint64       `cbor:"pow_nonce"`
}

var (
	canonicalEncoder = mustEncMode(cbor.EncOptions{Sort: cbor.SortCanonical})
	strictDecoder    = mustDecMode(cbor.DecOptions{
		DupMapKey:         cbor.DupMapKeyEnforcedAPF,
		ExtraReturnErrors: cbor.ExtraDecErrorUnknownField,
	})
)

func mustEncMode(options cbor.EncOptions) cbor.EncMode {
	mode, err := options.EncMode()
	if err != nil {
		panic(err)
	}
	return mode
}

// CanonicalMarshal encodes v with the protocol's deterministic CBOR encoder.
func CanonicalMarshal(v any) ([]byte, error) {
	return canonicalEncoder.Marshal(v)
}

// StrictDecode decodes data into v using the protocol's strict decoder, which
// rejects unknown fields, duplicate keys, and trailing data.
func StrictDecode(data []byte, v any) ([]byte, error) {
	return strictDecoder.UnmarshalFirst(data, v)
}

func mustDecMode(options cbor.DecOptions) cbor.DecMode {
	mode, err := options.DecMode()
	if err != nil {
		panic(err)
	}
	return mode
}

func CanonicalUnsignedBody(body UnsignedBody) ([]byte, error) {
	if err := validateUnsignedBody(body); err != nil {
		return nil, err
	}
	return canonicalEncoder.Marshal(body)
}

func DecodeUnsignedBody(data []byte) (UnsignedBody, error) {
	var body UnsignedBody
	rest, err := strictDecoder.UnmarshalFirst(data, &body)
	if err != nil {
		return UnsignedBody{}, err
	}
	if len(rest) != 0 {
		return UnsignedBody{}, errors.New("trailing CBOR data")
	}
	if err := validateUnsignedBody(body); err != nil {
		return UnsignedBody{}, err
	}
	canonical, err := CanonicalUnsignedBody(body)
	if err != nil || !bytes.Equal(canonical, data) {
		return UnsignedBody{}, errors.New("unsigned body is not canonical CBOR")
	}
	return body, nil
}

func validateUnsignedBody(body UnsignedBody) error {
	if body.ProtocolVersion != protocolVersion {
		return fmt.Errorf("unsupported protocol version %d", body.ProtocolVersion)
	}
	if len(body.StreamID) != 32 || len(body.NoteID) != 32 {
		return errors.New("stream_id and note_id must be 32 bytes")
	}
	if body.OperationType == "" {
		return errors.New("operation_type is required")
	}
	if len(body.AuthorPublicKey) != ed25519.PublicKeySize {
		return errors.New("author_public_key must be 32 bytes")
	}
	if len(body.OperationPayload) == 0 {
		return errors.New("operation_payload is required")
	}
	var payload any
	if rest, err := strictDecoder.UnmarshalFirst(body.OperationPayload, &payload); err != nil || len(rest) != 0 {
		return errors.New("operation_payload must be one canonical CBOR item")
	}
	canonicalPayload, err := canonicalEncoder.Marshal(payload)
	if err != nil || !bytes.Equal(canonicalPayload, body.OperationPayload) {
		return errors.New("operation_payload must be canonical CBOR")
	}
	return nil
}

func signingMessage(body UnsignedBody) []byte {
	encoded, _ := CanonicalUnsignedBody(body)
	return append([]byte("snapnotes/sign/v1"), encoded...)
}

func SignUnsignedBody(body UnsignedBody, privateKey ed25519.PrivateKey) ([]byte, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid Ed25519 private key")
	}
	if err := validateUnsignedBody(body); err != nil {
		return nil, err
	}
	return ed25519.Sign(privateKey, signingMessage(body)), nil
}

// VerifySignature checks an Ed25519 signature over the canonical signing message.
func VerifySignature(body UnsignedBody, signature, publicKey []byte) bool {
	if len(publicKey) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(publicKey, signingMessage(body), signature)
}

func TransactionID(body UnsignedBody) ([]byte, error) {
	encoded, err := CanonicalUnsignedBody(body)
	if err != nil {
		return nil, err
	}
	hash := sha256.New()
	hash.Write([]byte("snapnotes/txid/v1"))
	hash.Write(encoded)
	return hash.Sum(nil), nil
}

func PowPreimage(body UnsignedBody, transactionID []byte, epoch, nonce uint64) ([]byte, error) {
	if err := validateUnsignedBody(body); err != nil {
		return nil, err
	}
	if len(transactionID) != sha256.Size {
		return nil, errors.New("transaction_id must be 32 bytes")
	}
	preimage := make([]byte, 0, 16+32+32+32+16)
	preimage = append(preimage, []byte("snapnotes/pow/v1")...)
	preimage = append(preimage, body.StreamID...)
	preimage = append(preimage, transactionID...)
	preimage = append(preimage, body.AuthorPublicKey...)
	var encoded [16]byte
	binary.BigEndian.PutUint64(encoded[:8], epoch)
	binary.BigEndian.PutUint64(encoded[8:], nonce)
	preimage = append(preimage, encoded[:]...)
	return preimage, nil
}

func TransactionHash(body UnsignedBody, transactionID, signature []byte, epoch, nonce uint64) ([]byte, error) {
	if len(transactionID) != sha256.Size {
		return nil, errors.New("transaction_id must be 32 bytes")
	}
	if len(signature) != ed25519.SignatureSize {
		return nil, errors.New("signature must be 64 bytes")
	}
	if err := validateUnsignedBody(body); err != nil {
		return nil, err
	}
	encoded, err := canonicalEncoder.Marshal(transactionFields{body, transactionID, signature, epoch, nonce})
	if err != nil {
		return nil, err
	}
	hash := sha256.New()
	hash.Write([]byte("snapnotes/tx/v1"))
	hash.Write(encoded)
	return hash.Sum(nil), nil
}
