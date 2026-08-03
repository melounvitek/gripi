package push

import (
	"bytes"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"path/filepath"
	"strings"
	"sync"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/melounvitek/gripi/internal/keyedlock"
	"github.com/melounvitek/gripi/internal/state"
)

var vapidIdentityLocks keyedlock.Mutexes

type VAPIDKeys struct {
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
}

type VAPIDIdentity struct {
	file    *state.File
	lockKey string
	mu      sync.Mutex
	loaded  bool
	keys    VAPIDKeys
}

func NewVAPIDIdentity(path string) *VAPIDIdentity {
	return &VAPIDIdentity{file: state.NewFile(path), lockKey: absolutePath(path)}
}

func (identity *VAPIDIdentity) Keys() (VAPIDKeys, error) {
	identity.mu.Lock()
	defer identity.mu.Unlock()
	if identity.loaded {
		return identity.keys, nil
	}

	unlock := vapidIdentityLocks.Lock(identity.lockKey)
	defer unlock()

	keys, err := identity.load()
	if err != nil {
		return VAPIDKeys{}, err
	}
	identity.keys = keys
	identity.loaded = true
	return keys, nil
}

func (identity *VAPIDIdentity) load() (VAPIDKeys, error) {
	contents, exists, err := identity.file.Read()
	if err != nil {
		return VAPIDKeys{}, fmt.Errorf("read VAPID identity: %w", err)
	}
	if exists {
		return parseVAPIDIdentity(contents)
	}

	privateKey, publicKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return VAPIDKeys{}, fmt.Errorf("generate VAPID identity: %w", err)
	}
	contents, err = json.MarshalIndent(VAPIDKeys{PrivateKey: privateKey, PublicKey: publicKey}, "", "  ")
	if err != nil {
		return VAPIDKeys{}, fmt.Errorf("encode VAPID identity: %w", err)
	}
	contents = append(contents, '\n')
	if _, err := identity.file.CreateOnce(contents); err != nil {
		return VAPIDKeys{}, fmt.Errorf("persist VAPID identity: %w", err)
	}

	contents, exists, err = identity.file.Read()
	if err != nil {
		return VAPIDKeys{}, fmt.Errorf("read generated VAPID identity: %w", err)
	}
	if !exists {
		return VAPIDKeys{}, errors.New("VAPID identity disappeared during generation")
	}
	return parseVAPIDIdentity(contents)
}

func parseVAPIDIdentity(contents []byte) (VAPIDKeys, error) {
	var keys VAPIDKeys
	if err := json.Unmarshal(contents, &keys); err != nil {
		return VAPIDKeys{}, fmt.Errorf("VAPID identity is malformed: %w", err)
	}
	return parseVAPIDKeys(keys.PrivateKey, keys.PublicKey)
}

func parseVAPIDKeys(privateValue, publicValue string) (VAPIDKeys, error) {
	privateValue = strings.TrimSpace(privateValue)
	publicValue = strings.TrimSpace(publicValue)
	publicBytes, err := decodeVAPIDKey(publicValue)
	if err != nil || len(publicBytes) != 65 {
		return VAPIDKeys{}, errors.New("VAPID public key is malformed")
	}
	expectedPublic, err := publicKeyForPrivate(privateValue)
	if err != nil {
		return VAPIDKeys{}, err
	}
	expectedBytes, err := decodeVAPIDKey(expectedPublic)
	if err != nil || !bytes.Equal(publicBytes, expectedBytes) {
		return VAPIDKeys{}, errors.New("VAPID key pair does not match")
	}
	return VAPIDKeys{PrivateKey: privateValue, PublicKey: publicValue}, nil
}

func publicKeyForPrivate(privateValue string) (string, error) {
	privateBytes, err := decodeVAPIDKey(strings.TrimSpace(privateValue))
	if err != nil || len(privateBytes) != 32 {
		return "", errors.New("VAPID private key is malformed")
	}
	curve := elliptic.P256()
	private := new(big.Int).SetBytes(privateBytes)
	if private.Sign() <= 0 || private.Cmp(curve.Params().N) >= 0 {
		return "", errors.New("VAPID private key is outside the P-256 range")
	}
	x, y := curve.ScalarBaseMult(privateBytes)
	if x == nil || y == nil {
		return "", errors.New("VAPID private key does not produce a public key")
	}
	return base64.RawURLEncoding.EncodeToString(elliptic.Marshal(curve, x, y)), nil
}

func decodeVAPIDKey(value string) ([]byte, error) {
	for _, encoding := range []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	} {
		decoded, err := encoding.DecodeString(value)
		if err == nil {
			return decoded, nil
		}
	}
	return nil, errors.New("invalid base64 VAPID key")
}

func absolutePath(path string) string {
	absolute, err := filepath.Abs(path)
	if err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(path)
}
