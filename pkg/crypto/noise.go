package crypto

import (
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"

	"golang.org/x/crypto/blake2s"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const (
	ProtocolName = "Noise_IK_25519_ChaChaPoly_BLAKE2s"
	HashSize     = 32
)

// CipherState handles AEAD encryption/decryption using ChaCha20-Poly1305
type CipherState struct {
	aead cipher.AEAD
	key  Key
}

func NewCipherState(key Key) (*CipherState, error) {
	aead, err := chacha20poly1305.New(key[:])
	if err != nil {
		return nil, err
	}
	return &CipherState{
		aead: aead,
		key:  key,
	}, nil
}

// Encrypt encrypts plaintext with ChaCha20-Poly1305 using sequence number as nonce
func (cs *CipherState) Encrypt(dst []byte, pathID uint8, seq uint64, plaintext, ad []byte) []byte {
	var nonce [12]byte
	nonce[0] = 0
	nonce[1] = 0
	nonce[2] = 0
	nonce[3] = pathID
	binary.BigEndian.PutUint64(nonce[4:12], seq)

	return cs.aead.Seal(dst, nonce[:], plaintext, ad)
}

// Decrypt decrypts ciphertext with ChaCha20-Poly1305 using sequence number as nonce
func (cs *CipherState) Decrypt(dst []byte, pathID uint8, seq uint64, ciphertext, ad []byte) ([]byte, error) {
	var nonce [12]byte
	nonce[0] = 0
	nonce[1] = 0
	nonce[2] = 0
	nonce[3] = pathID
	binary.BigEndian.PutUint64(nonce[4:12], seq)

	return cs.aead.Open(dst, nonce[:], ciphertext, ad)
}

// SymmetricState encapsulates the Noise hashing and chaining key state
type SymmetricState struct {
	ck [HashSize]byte
	h  [HashSize]byte
}

func newSymmetricState() *SymmetricState {
	ss := &SymmetricState{}
	var protoBytes [HashSize]byte
	copy(protoBytes[:], []byte(ProtocolName))
	ss.h = protoBytes
	ss.ck = protoBytes
	return ss
}

func (ss *SymmetricState) mixHash(data []byte) {
	d, _ := blake2s.New256(nil)
	d.Write(ss.h[:])
	d.Write(data)
	copy(ss.h[:], d.Sum(nil))
}

func (ss *SymmetricState) mixKey(dh []byte) *CipherState {
	hashFn := func() hash.Hash {
		h, _ := blake2s.New256(nil)
		return h
	}
	reader := hkdf.New(hashFn, dh, ss.ck[:], nil)
	var newCK [HashSize]byte
	var newKey [KeySize]byte
	io.ReadFull(reader, newCK[:])
	io.ReadFull(reader, newKey[:])
	ss.ck = newCK

	cs, _ := NewCipherState(newKey)
	return cs
}

func (ss *SymmetricState) split() (*CipherState, *CipherState) {
	hashFn := func() hash.Hash {
		h, _ := blake2s.New256(nil)
		return h
	}
	reader := hkdf.New(hashFn, nil, ss.ck[:], []byte("split"))
	var k1, k2 [KeySize]byte
	io.ReadFull(reader, k1[:])
	io.ReadFull(reader, k2[:])

	cs1, _ := NewCipherState(k1)
	cs2, _ := NewCipherState(k2)
	return cs1, cs2
}

// HandshakeSession holds the derived transport cipher states
type HandshakeSession struct {
	TxState *CipherState // For transmitting packets
	RxState *CipherState // For receiving packets
}

// InitiatorHandshake runs the client-side Noise_IK initiator
type InitiatorHandshake struct {
	staticKey   KeyPair
	remoteRelay Key
	ephemeral   KeyPair
	sym         *SymmetricState
}

func NewInitiator(staticKey KeyPair, remoteRelayPub Key) (*InitiatorHandshake, error) {
	eph, err := GenerateKeyPair()
	if err != nil {
		return nil, err
	}
	sym := newSymmetricState()
	// Pre-messages: rs
	sym.mixHash(remoteRelayPub[:])

	return &InitiatorHandshake{
		staticKey:   staticKey,
		remoteRelay: remoteRelayPub,
		ephemeral:   eph,
		sym:         sym,
	}, nil
}

// CreateInitMessage creates Message 1: -> e, es, s, ss, payload
func (init *InitiatorHandshake) CreateInitMessage(payload []byte) ([]byte, error) {
	// e
	init.sym.mixHash(init.ephemeral.Public[:])

	// es = DH(e, rs)
	es, err := DH(init.ephemeral.Private, init.remoteRelay)
	if err != nil {
		return nil, fmt.Errorf("DH(e, rs) failed: %w", err)
	}
	cs := init.sym.mixKey(es[:])

	// s = Encrypt(static_c.pub)
	var encStatic [32 + 16]byte
	var nonce [12]byte
	cs.aead.Seal(encStatic[:0], nonce[:], init.staticKey.Public[:], init.sym.h[:])
	init.sym.mixHash(encStatic[:])

	// ss = DH(s, rs)
	ss, err := DH(init.staticKey.Private, init.remoteRelay)
	if err != nil {
		return nil, fmt.Errorf("DH(s, rs) failed: %w", err)
	}
	cs = init.sym.mixKey(ss[:])

	// payload
	encPayload := cs.aead.Seal(nil, nonce[:], payload, init.sym.h[:])
	init.sym.mixHash(encPayload)

	// Combine: e (32) + encStatic (48) + encPayload
	msg := make([]byte, 32+48+len(encPayload))
	copy(msg[0:32], init.ephemeral.Public[:])
	copy(msg[32:80], encStatic[:])
	copy(msg[80:], encPayload)

	return msg, nil
}

// ProcessRespMessage processes Message 2: <- e, ee, se, payload
func (init *InitiatorHandshake) ProcessRespMessage(msg []byte) (*HandshakeSession, []byte, error) {
	if len(msg) < 32+16 {
		return nil, nil, errors.New("response message too short")
	}

	var remoteEph Key
	copy(remoteEph[:], msg[0:32])
	init.sym.mixHash(remoteEph[:])

	// ee = DH(e, re)
	ee, err := DH(init.ephemeral.Private, remoteEph)
	if err != nil {
		return nil, nil, fmt.Errorf("DH(e, re) failed: %w", err)
	}
	init.sym.mixKey(ee[:])

	// se = DH(s, re)
	se, err := DH(init.staticKey.Private, remoteEph)
	if err != nil {
		return nil, nil, fmt.Errorf("DH(s, re) failed: %w", err)
	}
	cs := init.sym.mixKey(se[:])

	// decrypt payload
	var nonce [12]byte
	encPayload := msg[32:]
	payload, err := cs.aead.Open(nil, nonce[:], encPayload, init.sym.h[:])
	if err != nil {
		return nil, nil, fmt.Errorf("decrypt response payload failed: %w", err)
	}
	init.sym.mixHash(encPayload)

	// Split: Tx for client, Rx for client
	txState, rxState := init.sym.split()
	return &HandshakeSession{
		TxState: txState,
		RxState: rxState,
	}, payload, nil
}

// ResponderHandshake runs the relay-side Noise_IK responder
type ResponderHandshake struct {
	staticKey KeyPair
	sym       *SymmetricState
}

func NewResponder(staticKey KeyPair) *ResponderHandshake {
	sym := newSymmetricState()
	// Pre-messages: rs
	sym.mixHash(staticKey.Public[:])
	return &ResponderHandshake{
		staticKey: staticKey,
		sym:       sym,
	}
}

// ProcessInitMessage processes Message 1 from client and returns client public key and decrypted payload
func (resp *ResponderHandshake) ProcessInitMessage(msg []byte, replyPayload []byte) (*HandshakeSession, Key, []byte, []byte, error) {
	if len(msg) < 32+48+16 {
		return nil, Key{}, nil, nil, errors.New("init message too short")
	}

	var remoteEph Key
	copy(remoteEph[:], msg[0:32])
	resp.sym.mixHash(remoteEph[:])

	// es = DH(rs, e)
	es, err := DH(resp.staticKey.Private, remoteEph)
	if err != nil {
		return nil, Key{}, nil, nil, fmt.Errorf("DH(rs, e) failed: %w", err)
	}
	cs := resp.sym.mixKey(es[:])

	// decrypt s
	var nonce [12]byte
	encStatic := msg[32:80]
	clientStaticBytes, err := cs.aead.Open(nil, nonce[:], encStatic, resp.sym.h[:])
	if err != nil {
		return nil, Key{}, nil, nil, fmt.Errorf("decrypt client static key failed: %w", err)
	}
	var clientStatic Key
	copy(clientStatic[:], clientStaticBytes)
	resp.sym.mixHash(encStatic)

	// ss = DH(rs, s)
	ss, err := DH(resp.staticKey.Private, clientStatic)
	if err != nil {
		return nil, Key{}, nil, nil, fmt.Errorf("DH(rs, s) failed: %w", err)
	}
	cs = resp.sym.mixKey(ss[:])

	// decrypt payload
	encPayload := msg[80:]
	initPayload, err := cs.aead.Open(nil, nonce[:], encPayload, resp.sym.h[:])
	if err != nil {
		return nil, Key{}, nil, nil, fmt.Errorf("decrypt init payload failed: %w", err)
	}
	resp.sym.mixHash(encPayload)

	// Now generate responder ephemeral and reply message
	eph, err := GenerateKeyPair()
	if err != nil {
		return nil, Key{}, nil, nil, err
	}
	resp.sym.mixHash(eph.Public[:])

	// ee = DH(re, e)
	ee, err := DH(eph.Private, remoteEph)
	if err != nil {
		return nil, Key{}, nil, nil, fmt.Errorf("DH(re, e) failed: %w", err)
	}
	resp.sym.mixKey(ee[:])

	// se = DH(re, s)
	se, err := DH(eph.Private, clientStatic)
	if err != nil {
		return nil, Key{}, nil, nil, fmt.Errorf("DH(re, s) failed: %w", err)
	}
	cs = resp.sym.mixKey(se[:])

	// encrypt reply payload
	encReply := cs.aead.Seal(nil, nonce[:], replyPayload, resp.sym.h[:])
	resp.sym.mixHash(encReply)

	respMsg := make([]byte, 32+len(encReply))
	copy(respMsg[0:32], eph.Public[:])
	copy(respMsg[32:], encReply)

	// Split: Relay Rx = Client Tx, Relay Tx = Client Rx
	clientTx, clientRx := resp.sym.split()
	relaySession := &HandshakeSession{
		TxState: clientRx, // Relay transmits to Client using clientRx key
		RxState: clientTx, // Relay receives from Client using clientTx key
	}

	return relaySession, clientStatic, initPayload, respMsg, nil
}
