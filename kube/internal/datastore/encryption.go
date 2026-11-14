package datastore

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/eclipse-iofog/edgelet-kube/internal/bundle"
)

const encryptionFileName = "encryption-config.json"

// Encryption is the secrets-encryption decision for one boot.
type Encryption struct {
	// ProviderConfig is the encryption config file when the caller sets
	// encryption-provider-config. Empty tells the caller not to set it.
	ProviderConfig string
}

// EncryptionConfigPath is the secrets encryption file for dataDir.
// An empty dataDir uses the default data directory.
func EncryptionConfigPath(dataDir string) string {
	if dataDir == "" {
		dataDir = bundle.DefaultDataDir
	}
	return filepath.Join(dataDir, "server", "cred", encryptionFileName)
}

// PrepareEncryption applies the secrets-encryption flag before the database
// file is created. A false flag writes no key file. A true flag on a first
// boot writes encryption-config.json mode 0600, aescbc. A true flag when
// the database file exists and the key file does not is an error. A false
// flag when the key file exists is an error. An existing key file is kept.
func PrepareEncryption(dataDir string, enabled bool) (Encryption, error) {
	key := EncryptionConfigPath(dataDir)
	keyPresent, err := filePresent(key)
	if err != nil {
		return Encryption{}, err
	}
	if !enabled {
		if keyPresent {
			return Encryption{}, fmt.Errorf("secrets encryption is off and %s is present", key)
		}
		return Encryption{}, nil
	}
	if keyPresent {
		return Encryption{ProviderConfig: key}, nil
	}
	db := StateDB(dataDir)
	dbPresent, err := filePresent(db)
	if err != nil {
		return Encryption{}, err
	}
	if dbPresent {
		return Encryption{}, fmt.Errorf("secrets encryption is on, %s is present, and %s is absent", db, key)
	}
	if err := writeEncryptionConfig(key); err != nil {
		return Encryption{}, err
	}
	return Encryption{ProviderConfig: key}, nil
}

func filePresent(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func writeEncryptionConfig(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create encryption config directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { // #nosec G302 -- directory must be owner-traversable
		return fmt.Errorf("create encryption config directory: %w", err)
	}
	body, err := encryptionDocument()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".encryption-config-*")
	if err != nil {
		return fmt.Errorf("write encryption config: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write encryption config: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write encryption config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write encryption config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("write encryption config: %w", err)
	}
	return nil
}

type encryptionConfig struct {
	Kind       string               `json:"kind"`
	APIVersion string               `json:"apiVersion"`
	Resources  []encryptionResource `json:"resources"`
}

type encryptionResource struct {
	Resources []string             `json:"resources"`
	Providers []encryptionProvider `json:"providers"`
}

type identityProvider struct{}

type encryptionProvider struct {
	AESCBC   *aesCBC           `json:"aescbc,omitempty"`
	Identity *identityProvider `json:"identity,omitempty"`
}

type aesCBC struct {
	Keys []encryptionKey `json:"keys"`
}

type encryptionKey struct {
	Name   string `json:"name"`
	Secret string `json:"secret"`
}

func encryptionDocument() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate encryption key: %w", err)
	}
	doc := encryptionConfig{
		Kind:       "EncryptionConfiguration",
		APIVersion: "apiserver.config.k8s.io/v1",
		Resources: []encryptionResource{{
			Resources: []string{"secrets"},
			Providers: []encryptionProvider{
				{AESCBC: &aesCBC{Keys: []encryptionKey{{
					Name:   "aescbckey",
					Secret: base64.StdEncoding.EncodeToString(key),
				}}}},
				{Identity: &identityProvider{}},
			},
		}},
	}
	body, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encode encryption config: %w", err)
	}
	return body, nil
}
