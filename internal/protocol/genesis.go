package protocol

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
)

// DefaultGenesisTarget is the permissive PoW target (2^240) used by the MVP
// single node. The production genesis target must be benchmarked on a reference
// device before a real stream is created (see protocol-v1.md).
func DefaultGenesisTarget() []byte {
	target := make([]byte, 32)
	target[1] = 0x01
	return target
}

// GenesisResult is a fully constructed trust anchor for a single-node stream.
// Owners keep the private material; only the public parts and the genesis block
// are shared with other devices and the server.
type GenesisResult struct {
	StreamID                  []byte
	GenesisBlockHash          []byte
	PowTarget                 []byte
	OwnerSigningPublicKey     ed25519.PublicKey
	OwnerEncryptionPublicKey  *ecdh.PublicKey
	OwnerSigningKey           ed25519.PrivateKey
	OwnerEncryptionKey        *ecdh.PrivateKey
	StreamKey                 []byte // epoch-0 Stream Key, wrapped to the owner in the grant
	Block                     Block
}

type genesisPayload struct {
	OwnerSigningPublicKey     []byte `cbor:"owner_signing_public_key"`
	OwnerEncryptionPublicKey   []byte `cbor:"owner_encryption_public_key"`
	InitialPowTarget          []byte `cbor:"initial_pow_target"`
	InitialKeyEpoch           uint64 `cbor:"initial_key_epoch"`
	OwnerKeyGrant             []byte `cbor:"owner_key_grant"`
}

// GenesisPayload is the decoded genesis operation payload.
type GenesisPayload struct {
	OwnerSigningPublicKey    ed25519.PublicKey
	OwnerEncryptionPublicKey []byte
	InitialPowTarget         []byte
	InitialKeyEpoch          uint64
	OwnerKeyGrant            []byte
}

// DecodeGenesisPayload parses the canonical CBOR genesis operation payload.
func DecodeGenesisPayload(data []byte) (GenesisPayload, error) {
	var p genesisPayload
	if _, err := StrictDecode(data, &p); err != nil {
		return GenesisPayload{}, err
	}
	return GenesisPayload{
		OwnerSigningPublicKey:    p.OwnerSigningPublicKey,
		OwnerEncryptionPublicKey:  p.OwnerEncryptionPublicKey,
		InitialPowTarget:         p.InitialPowTarget,
		InitialKeyEpoch:          p.InitialKeyEpoch,
		OwnerKeyGrant:            p.OwnerKeyGrant,
	}, nil
}

// BuildGenesis creates a signed genesis block and the owner key material. The
// genesis block has height 0, a 32-zero previous_block_hash, exactly one genesis
// transaction, and wraps the epoch-0 Stream Key to the owner. The resulting
// GenesisBlockHash is the out-of-band trust anchor distributed with join requests.
func BuildGenesis(randomness io.Reader) (GenesisResult, error) {
	if randomness == nil {
		randomness = rand.Reader
	}
	ownerSigningPub, ownerSigningPriv, err := ed25519.GenerateKey(randomness)
	if err != nil {
		return GenesisResult{}, err
	}
	ownerEncPriv, err := ecdh.X25519().GenerateKey(randomness)
	if err != nil {
		return GenesisResult{}, err
	}
	streamKey := make([]byte, 32)
	if _, err := io.ReadFull(randomness, streamKey); err != nil {
		return GenesisResult{}, err
	}
	streamID := make([]byte, 32)
	if _, err := io.ReadFull(randomness, streamID); err != nil {
		return GenesisResult{}, err
	}

	// Wrap the epoch-0 Stream Key to the owner so the owner can decrypt it.
	grantAAD := EnvelopeAAD{
		ProtocolVersion: 1,
		StreamID:        streamID,
		NoteID:          make([]byte, 32),
		TransactionID:   make([]byte, 32),
		KeyEpoch:        0,
		Field:           "key_envelope",
	}
	ownerKeyGrant, err := EncryptKeyEnvelope(ownerEncPriv.PublicKey(), grantAAD, streamKey, randomness)
	if err != nil {
		return GenesisResult{}, err
	}

	payload := genesisPayload{
		OwnerSigningPublicKey:    ownerSigningPub,
		OwnerEncryptionPublicKey: ownerEncPriv.PublicKey().Bytes(),
		InitialPowTarget:         DefaultGenesisTarget(),
		InitialKeyEpoch:          0,
		OwnerKeyGrant:            ownerKeyGrant,
	}
	payloadCBOR, err := CanonicalMarshal(payload)
	if err != nil {
		return GenesisResult{}, err
	}

	body := UnsignedBody{
		ProtocolVersion:   1,
		StreamID:          streamID,
		NoteID:            make([]byte, 32), // mandatory zero note_id for membership/key ops
		OperationType:     "genesis",
		OperationPayload:  payloadCBOR,
		ClientCreatedAt:   0,
		AuthorPublicKey:   ownerSigningPub,
	}
	txID, err := TransactionID(body)
	if err != nil {
		return GenesisResult{}, err
	}
	sig, err := SignUnsignedBody(body, ownerSigningPriv)
	if err != nil {
		return GenesisResult{}, err
	}
	if len(sig) != ed25519.SignatureSize {
		return GenesisResult{}, errors.New("genesis signature has wrong length")
	}
	txHash, err := TransactionHash(body, txID, sig, 0, 0)
	if err != nil {
		return GenesisResult{}, err
	}

	mmrRoot := MMRRootFromLeaves([][]byte{LeafHash(txHash)})
	header := BlockHeader{
		ProtocolVersion:   1,
		Height:            0,
		PreviousBlockHash: make([]byte, 32),
		TransactionCount:  1,
		MMRRoot:           mmrRoot,
		Timestamp:         0,
	}
	nonce, blockHash, ok := MineBlock(header, DefaultGenesisTarget(), 0, randomness)
	if !ok {
		return GenesisResult{}, errors.New("genesis mining failed to meet target")
	}
	header.Nonce = nonce
	header.PowTarget = DefaultGenesisTarget()

	return GenesisResult{
		StreamID:                 streamID,
		GenesisBlockHash:         blockHash,
		PowTarget:                DefaultGenesisTarget(),
		OwnerSigningPublicKey:    ownerSigningPub,
		OwnerEncryptionPublicKey: ownerEncPriv.PublicKey(),
		OwnerSigningKey:          ownerSigningPriv,
		OwnerEncryptionKey:       ownerEncPriv,
		StreamKey:                streamKey,
		Block: Block{
			Header:     header,
			BlockHash:  blockHash,
			Transaction: SignedTransaction{
				UnsignedBody:  body,
				TransactionID: txID,
				Signature:     sig,
				PowEpoch:      0,
				PowNonce:      0,
			},
		},
	}, nil
}
