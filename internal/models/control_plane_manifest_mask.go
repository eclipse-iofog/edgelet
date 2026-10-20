package models

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

const controlPlaneSecretMask = "***"

// DecodeControlPlaneManifestYAML decodes ControlPlane YAML without semantic validation.
func DecodeControlPlaneManifestYAML(manifest string) (*ControlPlaneManifest, error) {
	doc := &ControlPlaneManifest{}
	dec := yaml.NewDecoder(bytes.NewReader([]byte(strings.TrimSpace(manifest))))
	dec.KnownFields(true)
	if err := dec.Decode(doc); err != nil {
		return nil, fmt.Errorf("invalid manifest YAML: %w", err)
	}
	doc.NormalizeDefaults()
	return doc, nil
}

// MaskSecrets redacts sensitive ControlPlane manifest fields in place.
func (m *ControlPlaneManifest) MaskSecrets() {
	if m == nil {
		return
	}
	if m.Spec.Auth != nil {
		if m.Spec.Auth.Bootstrap != nil {
			m.Spec.Auth.Bootstrap.Password = maskSecretValue(m.Spec.Auth.Bootstrap.Password)
		}
		if m.Spec.Auth.Client != nil {
			m.Spec.Auth.Client.Secret = maskSecretValue(m.Spec.Auth.Client.Secret)
		}
		if m.Spec.Auth.SessionStore != nil {
			m.Spec.Auth.SessionStore.Secret = maskSecretValue(m.Spec.Auth.SessionStore.Secret)
		}
		m.Spec.Auth.IssuerCA = maskSecretValue(m.Spec.Auth.IssuerCA)
	}
	if m.Spec.Database != nil {
		m.Spec.Database.Password = maskSecretValue(m.Spec.Database.Password)
		m.Spec.Database.CA = maskSecretValue(m.Spec.Database.CA)
	}
	if m.Spec.TLS != nil && m.Spec.TLS.Base64 != nil {
		m.Spec.TLS.Base64.Cert = maskSecretValue(m.Spec.TLS.Base64.Cert)
		m.Spec.TLS.Base64.Key = maskSecretValue(m.Spec.TLS.Base64.Key)
		m.Spec.TLS.Base64.CA = maskSecretValue(m.Spec.TLS.Base64.CA)
	}
	if m.Spec.Vault != nil {
		if m.Spec.Vault.Hashicorp != nil {
			m.Spec.Vault.Hashicorp.Token = maskSecretValue(m.Spec.Vault.Hashicorp.Token)
		}
		if m.Spec.Vault.AWS != nil {
			m.Spec.Vault.AWS.AccessKeyID = maskSecretValue(m.Spec.Vault.AWS.AccessKeyID)
			m.Spec.Vault.AWS.AccessKey = maskSecretValue(m.Spec.Vault.AWS.AccessKey)
		}
		if m.Spec.Vault.Azure != nil {
			m.Spec.Vault.Azure.ClientSecret = maskSecretValue(m.Spec.Vault.Azure.ClientSecret)
		}
		if m.Spec.Vault.Google != nil {
			m.Spec.Vault.Google.Credentials = maskSecretValue(m.Spec.Vault.Google.Credentials)
		}
	}
}

// MaskedControlPlaneManifestYAML returns manifest YAML with secrets redacted.
func MaskedControlPlaneManifestYAML(manifest string) (string, error) {
	doc, err := DecodeControlPlaneManifestYAML(manifest)
	if err != nil {
		return "", err
	}
	doc.MaskSecrets()
	out, err := yaml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("failed to marshal masked manifest: %w", err)
	}
	return string(out), nil
}

func maskSecretValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return value
	}
	return controlPlaneSecretMask
}
