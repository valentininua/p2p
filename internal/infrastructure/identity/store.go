package identity

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/libp2p/go-libp2p/core/crypto"
)

const (
	dirName     = ".p2pmessenger"
	keyFileName = "identity.key"
)

func KeyPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return keyFileName
	}

	return filepath.Join(home, dirName, keyFileName)
}

func LoadOrCreate() (crypto.PrivKey, bool, error) {
	path := KeyPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, err
	}

	data, err := os.ReadFile(path)
	if err == nil {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if err != nil {
			return nil, false, fmt.Errorf("decode key: %w", err)
		}

		priv, err := crypto.UnmarshalPrivateKey(raw)
		if err != nil {
			return nil, false, fmt.Errorf("unmarshal key: %w", err)
		}

		return priv, false, nil
	}

	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, fmt.Errorf("read key: %w", err)
	}

	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, false, fmt.Errorf("generate key: %w", err)
	}

	raw, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		return nil, false, fmt.Errorf("marshal key: %w", err)
	}

	encoded := base64.StdEncoding.EncodeToString(raw)
	if err := os.WriteFile(path, []byte(encoded+"\n"), 0o600); err != nil {
		return nil, false, fmt.Errorf("save key: %w", err)
	}

	return priv, true, nil
}

func Delete() error {
	if err := os.Remove(KeyPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	return nil
}
