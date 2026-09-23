package resourceconsumption

import (
	"context"
	"runtime"
	"strings"
	"sync"

	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/shirou/gopsutil/v4/host"
)

type hostIdentity struct {
	os            string
	osVersion     string
	kernelVersion string
}

var (
	hostIdentityMu     sync.RWMutex
	cachedHostIdentity hostIdentity
	hostIdentityReady  bool
)

func (rcm *Manager) ensureHostIdentity() hostIdentity {
	hostIdentityMu.RLock()
	if hostIdentityReady {
		id := cachedHostIdentity
		hostIdentityMu.RUnlock()
		return id
	}
	hostIdentityMu.RUnlock()

	id := collectHostIdentity()
	hostIdentityMu.Lock()
	if !hostIdentityReady {
		cachedHostIdentity = id
		hostIdentityReady = true
	} else {
		id = cachedHostIdentity
	}
	hostIdentityMu.Unlock()
	return id
}

func collectHostIdentity() hostIdentity {
	id := hostIdentity{
		os: strings.ToLower(strings.TrimSpace(runtime.GOOS)),
	}
	info, err := host.InfoWithContext(context.Background())
	if err != nil {
		logging.LogError(moduleName, "Error reading host OS identity", err)
		return id
	}
	if pretty := hostOSReleasePrettyName(); pretty != "" {
		id.osVersion = pretty
	} else {
		id.osVersion = formatHostOSVersion(info.Platform, info.PlatformVersion)
	}
	if runtime.GOOS == "linux" {
		id.kernelVersion = strings.TrimSpace(info.KernelVersion)
	}
	return id
}

func formatHostOSVersion(platform, platformVersion string) string {
	platform = strings.TrimSpace(platform)
	version := strings.TrimSpace(platformVersion)
	if platform != "" && version != "" {
		return displayPlatformFamily(platform) + " " + version
	}
	if version != "" {
		return version
	}
	if platform != "" {
		return displayPlatformFamily(platform)
	}
	return ""
}

func displayPlatformFamily(family string) string {
	family = strings.TrimSpace(family)
	if family == "" {
		return ""
	}
	runes := []rune(strings.ToLower(family))
	runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
	return string(runes)
}
