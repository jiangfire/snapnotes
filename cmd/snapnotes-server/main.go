package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"log"
	"net/http"
	"strings"

	"github.com/jiangfire/snapnotes/internal/api"
	"github.com/jiangfire/snapnotes/internal/protocol"
)

func main() {
	listen := flag.String("listen", "0.0.0.0:8333", "listen address")
	authKeys := flag.String("authorized-keys", "", "comma-separated hex Ed25519 public keys allowed to write")
	genKey := flag.Bool("gen-key", false, "generate a signing key pair, print the public key, and exit")
	genesisB64 := flag.String("genesis", "", "base64 of a previously generated genesis block; reuse across restarts to keep the same trust anchor")
	dataDir := flag.String("data-dir", ".snapnotes-server", "directory for the on-disk ledger (blocks, MMR leaves, members); created if missing")
	peerURL := flag.String("peer", "", "URL of a peer snapnotes-server to sync the chain from at startup (headers-first pull + chain selection)")
	flag.Parse()

	if *genKey {
		pub, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("public_key=%s", hex.EncodeToString(pub))
		return
	}

	gen, err := loadGenesis(*genesisB64)
	if err != nil {
		log.Fatalf("genesis: %v", err)
	}

	var keys []ed25519.PublicKey
	for _, raw := range strings.Split(*authKeys, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		b, err := hex.DecodeString(raw)
		if err != nil || len(b) != ed25519.PublicKeySize {
			log.Fatalf("invalid authorized key %q", raw)
		}
		keys = append(keys, ed25519.PublicKey(b))
	}
	if len(keys) == 0 {
		log.Println("warning: no authorized keys configured; all writes will be rejected")
	}

	server, err := api.NewServerWithPeer([]api.StreamConfig{{
		StreamID:       gen.StreamID,
		Genesis:        gen.Block,
		AuthorizedKeys: keys,
	}}, *dataDir, *peerURL)
	if err != nil {
		log.Fatal(err)
	}
	defer server.Close()

	if *peerURL != "" {
		log.Printf("peer_sync_url=%s", *peerURL)
	}

	log.Printf("stream_id=%s", base64.RawURLEncoding.EncodeToString(gen.StreamID))
	log.Printf("genesis_block_hash=%s", base64.RawURLEncoding.EncodeToString(gen.GenesisBlockHash))
	log.Printf("owner_signing_public_key=%s", hex.EncodeToString(gen.OwnerSigningPublicKey))
	if *genesisB64 == "" {
		blockCBOR, err := protocol.MarshalBlock(gen.Block)
		if err != nil {
			log.Fatalf("marshal genesis: %v", err)
		}
		log.Printf("genesis_block=%s", base64.StdEncoding.EncodeToString(blockCBOR))
		log.Println("pass the genesis_block value via -genesis on restart to keep the same trust anchor")
	}
	log.Printf("ledger_data_dir=%s", *dataDir)
	log.Printf("snapnotes-server listening on %s", *listen)
	if err := http.ListenAndServe(*listen, server.Handler()); err != nil {
		log.Fatal(err)
	}
}

func loadGenesis(encoded string) (protocol.GenesisResult, error) {
	if encoded == "" {
		return protocol.BuildGenesis(rand.Reader)
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return protocol.GenesisResult{}, err
	}
	block, err := protocol.DecodeBlock(data)
	if err != nil {
		return protocol.GenesisResult{}, err
	}
	gp, err := protocol.DecodeGenesisPayload(block.Transaction.UnsignedBody.OperationPayload)
	if err != nil {
		return protocol.GenesisResult{}, err
	}
	return protocol.GenesisResult{
		StreamID:              block.Transaction.UnsignedBody.StreamID,
		GenesisBlockHash:      block.BlockHash,
		PowTarget:             gp.InitialPowTarget,
		OwnerSigningPublicKey: block.Transaction.UnsignedBody.AuthorPublicKey,
		Block:                 block,
	}, nil
}
