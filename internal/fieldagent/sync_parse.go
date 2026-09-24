package fieldagent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

func applyMicroserviceProcessArgv(ms *models.Microservice, data map[string]any) {
	if ms == nil || data == nil {
		return
	}
	cmd, cmdSet := parseOptionalArgv(data, "cmd")
	commands, commandsSet := parseOptionalArgv(data, "commands")
	var chosen *[]string
	if cmdSet {
		chosen = cmd
	}
	if commandsSet {
		chosen = commands
	}
	if models.UsesImageDefault(chosen) {
		ms.Commands = nil
	} else {
		ms.Commands = chosen
		ms.Args = append([]string{}, (*chosen)...)
	}

	entrypoint, entrySet := parseOptionalArgv(data, "entrypoint")
	if !entrySet || models.UsesImageDefault(entrypoint) {
		ms.Entrypoint = nil
		return
	}
	ms.Entrypoint = entrypoint
}

func applyMicroserviceContainerExtras(ms *models.Microservice, data map[string]any) {
	if ms == nil || data == nil {
		return
	}
	if runAsGroup, ok := data["runAsGroup"].(string); ok && strings.TrimSpace(runAsGroup) != "" {
		ms.RunAsGroup = &runAsGroup
	}
	if workingDir, ok := data["workingDir"].(string); ok && strings.TrimSpace(workingDir) != "" {
		ms.WorkingDir = &workingDir
	}
	if readOnly, ok := data["readOnlyRootFilesystem"].(bool); ok {
		ms.ReadOnlyRootFilesystem = readOnly
	}
	if cpus, ok := jsonFloat64(data["cpus"]); ok {
		ms.Cpus = &cpus
	}
	if reservation, ok := jsonInt64(data["memoryReservation"]); ok {
		ms.SetMemoryReservationMB(&reservation)
	}
	if swap, ok := jsonInt64(data["memorySwap"]); ok {
		ms.SetMemorySwapMB(&swap)
	}
	if shm, ok := jsonInt64(data["shmSize"]); ok {
		ms.SetShmSizeMB(&shm)
	}
	if sysctls := parseStringMap(data["sysctls"]); len(sysctls) > 0 {
		ms.Sysctls = sysctls
	}
	if ulimits := parseUlimits(data["ulimits"]); len(ulimits) > 0 {
		ms.Ulimits = ulimits
	}
	if devices := parseDevices(data["devices"]); len(devices) > 0 {
		ms.Devices = devices
	}
	if tmpfs := parseTmpfs(data["tmpfs"]); len(tmpfs) > 0 {
		ms.Tmpfs = tmpfs
	}
	if raw, ok := data["models"].(map[string]any); ok {
		ms.Models = parseModelCatalog(raw)
	}
	if raw, ok := data["knowledge"].(map[string]any); ok {
		ms.Knowledge = parseKnowledgeCatalog(raw)
	}
}

func parseOptionalArgv(data map[string]any, key string) (*[]string, bool) {
	raw, ok := data[key]
	if !ok {
		return nil, false
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, true
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return &out, true
}

func parseModelCatalog(raw map[string]any) *models.ModelCatalog {
	if raw == nil {
		return nil
	}
	cat := &models.ModelCatalog{}
	if bindPath, ok := raw["bindPath"].(string); ok {
		cat.BindPath = bindPath
	}
	if permissions, ok := raw["permissions"].(string); ok {
		cat.Permissions = permissions
	}
	if items, ok := raw["items"].([]any); ok {
		cat.Items = make([]models.ModelCatalogItem, 0, len(items))
		for _, item := range items {
			itemMap, ok := item.(map[string]any)
			if !ok {
				continue
			}
			name, ok := itemMap["name"].(string)
			if !ok {
				continue
			}
			cat.Items = append(cat.Items, models.ModelCatalogItem{Name: name})
		}
	}
	cat.NormalizeDefaults()
	if !cat.HasItems() && cat.BindPath == "" {
		return nil
	}
	return cat
}

func parseKnowledgeCatalog(raw map[string]any) *models.KnowledgeCatalog {
	if raw == nil {
		return nil
	}
	cat := &models.KnowledgeCatalog{}
	if bindPath, ok := raw["bindPath"].(string); ok {
		cat.BindPath = bindPath
	}
	if permissions, ok := raw["permissions"].(string); ok {
		cat.Permissions = permissions
	}
	if items, ok := raw["items"].([]any); ok {
		cat.Items = make([]models.KnowledgeCatalogItem, 0, len(items))
		for _, item := range items {
			itemMap, ok := item.(map[string]any)
			if !ok {
				continue
			}
			name, ok := itemMap["name"].(string)
			if !ok {
				continue
			}
			cat.Items = append(cat.Items, models.KnowledgeCatalogItem{Name: name})
		}
	}
	cat.NormalizeDefaults()
	if !cat.HasItems() && cat.BindPath == "" {
		return nil
	}
	return cat
}

func parseStringMap(raw any) map[string]string {
	src, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(src))
	for key, value := range src {
		k := strings.TrimSpace(key)
		if k == "" {
			continue
		}
		out[k] = jsonString(value)
	}
	return out
}

func parseUlimits(raw any) map[string]models.Ulimit {
	src, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]models.Ulimit, len(src))
	for key, value := range src {
		obj, ok := value.(map[string]any)
		if !ok {
			continue
		}
		soft, okSoft := jsonInt64(obj["soft"])
		hard, okHard := jsonInt64(obj["hard"])
		if !okSoft || !okHard {
			continue
		}
		out[key] = models.Ulimit{Soft: soft, Hard: hard}
	}
	return out
}

func parseDevices(raw any) []models.DeviceMapping {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]models.DeviceMapping, 0, len(items))
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		dev := models.DeviceMapping{}
		if hostPath, ok := obj["hostPath"].(string); ok {
			dev.HostPath = hostPath
		}
		if containerPath, ok := obj["containerPath"].(string); ok {
			dev.ContainerPath = containerPath
		}
		if permissions, ok := obj["permissions"].(string); ok {
			dev.Permissions = permissions
		}
		out = append(out, dev)
	}
	return out
}

func parseTmpfs(raw any) []models.TmpfsMount {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]models.TmpfsMount, 0, len(items))
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		mount := models.TmpfsMount{}
		if containerPath, ok := obj["containerPath"].(string); ok {
			mount.ContainerPath = containerPath
		}
		if size, ok := jsonInt64(obj["size"]); ok {
			mount.Size = &size
		}
		if mode, ok := obj["mode"].(string); ok {
			mount.Mode = mode
		} else if modeNum, ok := jsonInt64(obj["mode"]); ok {
			mount.Mode = fmt.Sprintf("%o", modeNum)
		}
		out = append(out, mount)
	}
	return out
}

func jsonInt(v any) (int, bool) {
	n, ok := jsonInt64(v)
	if !ok {
		return 0, false
	}
	return int(n), true
}

func jsonInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	case int64:
		return n, true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	default:
		return 0, false
	}
}

func jsonFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func jsonString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case nil:
		return ""
	default:
		return fmt.Sprint(s)
	}
}
