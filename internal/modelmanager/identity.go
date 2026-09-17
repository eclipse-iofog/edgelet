package modelmanager

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/modelpull"
	"github.com/eclipse-iofog/edgelet/internal/models"
)

// IdentityKey is the pull identity: registry id + type + repo + revision + hash(files).
func IdentityKey(registryID int, registryType, repo, revision string, files []string) string {
	return fmt.Sprintf("%d|%s|%s|%s|%s",
		registryID,
		models.NormalizeRegistryType(registryType),
		strings.TrimSpace(repo),
		strings.TrimSpace(revision),
		filesHash(files),
	)
}

func identityForDesired(reg *models.Registry, row *models.LocalModel) string {
	if row == nil {
		return ""
	}
	regType := models.RegistryTypeOCI
	if reg != nil {
		regType = reg.NormalizedType()
	}
	return IdentityKey(row.RegistryID, regType, row.Repo, row.Revision, row.Files())
}

func specFingerprint(row *models.LocalModel) string {
	if row == nil {
		return ""
	}
	return fmt.Sprintf("%d|%s|%s|%s|%s",
		row.RegistryID,
		strings.TrimSpace(row.Repo),
		strings.TrimSpace(row.Revision),
		filesHash(row.Files()),
		strings.ToLower(strings.TrimSpace(row.Format)),
	)
}

func filesHash(files []string) string {
	cleaned := make([]string, 0, len(files))
	for _, name := range files {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		cleaned = append(cleaned, name)
	}
	slices.Sort(cleaned)
	sum := sha256.New()
	for _, name := range cleaned {
		_, _ = sum.Write([]byte(name))
		_, _ = sum.Write([]byte{0})
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func identityMatchesManifest(desiredKey string, onDisk modelpull.OnDiskManifest, reg *models.Registry, row *models.LocalModel) bool {
	if strings.TrimSpace(onDisk.IdentityKey) != "" {
		return onDisk.IdentityKey == desiredKey
	}
	if row == nil || reg == nil {
		return false
	}
	return onDisk.RegistryID == row.RegistryID &&
		models.NormalizeRegistryType(onDisk.RegistryType) == reg.NormalizedType() &&
		strings.TrimSpace(onDisk.Repo) == strings.TrimSpace(row.Repo) &&
		strings.TrimSpace(onDisk.RequestedRevision) == strings.TrimSpace(row.Revision)
}
