package models

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// ValidateModelCatalog checks catalog shape, permissions, duplicates, and path collisions.
// Omitted catalog or empty items do not require bindPath. Permissions default to ro when items are present.
func ValidateModelCatalog(c *ModelCatalog, volumeDests, tmpfsPaths []string) error {
	if c == nil || !c.HasItems() {
		return nil
	}
	c.NormalizeDefaults()
	if c.BindPath == "" {
		return errors.New("spec.models.bindPath is required when items are set")
	}
	if !path.IsAbs(c.BindPath) {
		return errors.New("spec.models.bindPath must be an absolute container path")
	}
	switch c.Permissions {
	case ModelCatalogPermRO, ModelCatalogPermRW:
	default:
		return errors.New("spec.models.permissions must be ro or rw")
	}

	seen := make(map[string]struct{}, len(c.Items))
	occupied := make(map[string]string)
	for _, dest := range volumeDests {
		key := cleanContainerPath(dest)
		if key != "" {
			occupied[key] = "volume"
		}
	}
	for _, dest := range tmpfsPaths {
		key := cleanContainerPath(dest)
		if key != "" {
			occupied[key] = "tmpfs"
		}
	}

	bind := cleanContainerPath(c.BindPath)
	if kind, ok := occupied[bind]; ok {
		return fmt.Errorf("spec.models.bindPath %q collides with a %s container path", c.BindPath, kind)
	}

	for i, item := range c.Items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			return fmt.Errorf("spec.models.items[%d].name is required", i)
		}
		if len(name) > 63 || !localDeployNamePattern.MatchString(name) {
			return fmt.Errorf("spec.models.items[%d].name must match DNS-1123 label format", i)
		}
		if _, dup := seen[name]; dup {
			return fmt.Errorf("spec.models.items[%d].name %q is duplicated", i, name)
		}
		seen[name] = struct{}{}
		itemPath := path.Join(bind, name)
		if kind, ok := occupied[itemPath]; ok {
			return fmt.Errorf("catalog path %q collides with a %s container path", itemPath, kind)
		}
	}
	return nil
}

// ValidateKnowledgeCatalog checks knowledge catalog shape, permissions, duplicates, and path collisions.
// Occupied paths include volumes, tmpfs, and model catalog bindPath / {bindPath}/{name}.
// Omitted catalog or empty items do not require bindPath. Permissions default to ro when items are present.
func ValidateKnowledgeCatalog(c *KnowledgeCatalog, volumeDests, tmpfsPaths, modelPaths []string) error {
	if c == nil || !c.HasItems() {
		return nil
	}
	c.NormalizeDefaults()
	if c.BindPath == "" {
		return errors.New("spec.knowledge.bindPath is required when items are set")
	}
	if !path.IsAbs(c.BindPath) {
		return errors.New("spec.knowledge.bindPath must be an absolute container path")
	}
	switch c.Permissions {
	case KnowledgeCatalogPermRO, KnowledgeCatalogPermRW:
	default:
		return errors.New("spec.knowledge.permissions must be ro or rw")
	}

	seen := make(map[string]struct{}, len(c.Items))
	occupied := make(map[string]string)
	for _, dest := range volumeDests {
		key := cleanContainerPath(dest)
		if key != "" {
			occupied[key] = "volume"
		}
	}
	for _, dest := range tmpfsPaths {
		key := cleanContainerPath(dest)
		if key != "" {
			occupied[key] = "tmpfs"
		}
	}
	for _, dest := range modelPaths {
		key := cleanContainerPath(dest)
		if key != "" {
			occupied[key] = "models catalog"
		}
	}

	bind := cleanContainerPath(c.BindPath)
	if kind, ok := occupied[bind]; ok {
		return fmt.Errorf("spec.knowledge.bindPath %q collides with a %s container path", c.BindPath, kind)
	}

	for i, item := range c.Items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			return fmt.Errorf("spec.knowledge.items[%d].name is required", i)
		}
		if len(name) > 63 || !localDeployNamePattern.MatchString(name) {
			return fmt.Errorf("spec.knowledge.items[%d].name must match DNS-1123 label format", i)
		}
		if _, dup := seen[name]; dup {
			return fmt.Errorf("spec.knowledge.items[%d].name %q is duplicated", i, name)
		}
		seen[name] = struct{}{}
		itemPath := path.Join(bind, name)
		if kind, ok := occupied[itemPath]; ok {
			return fmt.Errorf("catalog path %q collides with a %s container path", itemPath, kind)
		}
	}
	return nil
}

// collectModelCatalogPaths returns bindPath and {bindPath}/{name} for a models catalog.
func collectModelCatalogPaths(c *ModelCatalog) []string {
	if c == nil || !c.HasItems() {
		return nil
	}
	c.NormalizeDefaults()
	bind := cleanContainerPath(c.BindPath)
	out := make([]string, 0, 1+len(c.Items))
	if bind != "" {
		out = append(out, bind)
	}
	for _, item := range c.Items {
		name := strings.TrimSpace(item.Name)
		if name == "" || bind == "" {
			continue
		}
		out = append(out, path.Join(bind, name))
	}
	return out
}

// ValidateWorkloadCatalogs validates models and knowledge catalogs, including collisions between them.
func ValidateWorkloadCatalogs(models *ModelCatalog, knowledge *KnowledgeCatalog, volumeDests, tmpfsPaths []string) error {
	if err := ValidateModelCatalog(models, volumeDests, tmpfsPaths); err != nil {
		return err
	}
	return ValidateKnowledgeCatalog(knowledge, volumeDests, tmpfsPaths, collectModelCatalogPaths(models))
}

// ValidateCatalogModelSources checks that every named item exists with the required source.
func ValidateCatalogModelSources(c *ModelCatalog, requiredSource string, lookup ModelSourceLookup) error {
	if c == nil || !c.HasItems() {
		return nil
	}
	want := strings.ToLower(strings.TrimSpace(requiredSource))
	if !ValidModelSource(want) {
		return fmt.Errorf("invalid required model source %q", requiredSource)
	}
	if lookup == nil {
		return nil
	}
	for _, item := range c.Items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		source, exists, err := lookup(name)
		if err != nil {
			return err
		}
		if !exists {
			return &ErrModelSourceScope{Name: name, Want: want, Missing: true}
		}
		have := strings.ToLower(strings.TrimSpace(source))
		if have != want {
			return &ErrModelSourceScope{Name: name, Want: want, Have: have}
		}
	}
	return nil
}

func collectTmpfsPaths(tmpfs []TmpfsMount) []string {
	out := make([]string, 0, len(tmpfs))
	for _, t := range tmpfs {
		if dest := strings.TrimSpace(t.ContainerPath); dest != "" {
			out = append(out, dest)
		}
	}
	return out
}

func collectVolumeDestsFromMappings(volumes []*VolumeMapping) []string {
	out := make([]string, 0, len(volumes))
	for _, v := range volumes {
		if v == nil {
			continue
		}
		if dest := strings.TrimSpace(v.ContainerDestination); dest != "" {
			out = append(out, dest)
		}
	}
	return out
}

func validateManifestContainerFields(m *LocalDeployManifest) error {
	c := m.Spec.Container
	if err := validateRunAsUserGroup(c.RunAsUser, c.RunAsGroup); err != nil {
		return err
	}
	if err := validateSysctls(c.Sysctls, c.HostNetworkMode, ipcModeIsHost(c.IpcMode)); err != nil {
		return err
	}
	if err := validateUlimits(c.Ulimits); err != nil {
		return err
	}
	if c.Cpus != nil && *c.Cpus <= 0 {
		return errors.New("spec.container.cpus must be greater than 0")
	}
	if c.MemoryReservation != nil && *c.MemoryReservation <= 0 {
		return errors.New("spec.container.memoryReservation must be greater than 0")
	}
	if c.ShmSize != nil && *c.ShmSize <= 0 {
		return errors.New("spec.container.shmSize must be greater than 0")
	}
	if err := validateMemorySwap(c.MemoryLimit > 0, c.MemorySwap); err != nil {
		return fmt.Errorf("spec.container.%s", err.Error())
	}
	for i, d := range c.Devices {
		if err := validateDeviceMapping(d, i); err != nil {
			return err
		}
		c.Devices[i].HostPath = strings.TrimSpace(d.HostPath)
		c.Devices[i].ContainerPath = strings.TrimSpace(d.ContainerPath)
		perms := strings.TrimSpace(d.Permissions)
		if perms == "" {
			perms = "rwm"
		}
		c.Devices[i].Permissions = perms
	}
	for i, t := range c.Tmpfs {
		if err := validateTmpfsMount(t, i); err != nil {
			return err
		}
	}
	if wd := strings.TrimSpace(c.WorkingDir); wd != "" && !path.IsAbs(wd) {
		return errors.New("spec.container.workingDir must be an absolute container path")
	}
	return nil
}

func validateMicroserviceContainerFields(m *Microservice) error {
	runAsUser := ""
	if m.RunAsUser != nil {
		runAsUser = *m.RunAsUser
	}
	runAsGroup := ""
	if m.RunAsGroup != nil {
		runAsGroup = *m.RunAsGroup
	}
	if err := validateRunAsUserGroup(runAsUser, runAsGroup); err != nil {
		return err
	}
	ipcMode := ""
	if m.IpcMode != nil {
		ipcMode = *m.IpcMode
	}
	if err := validateSysctls(m.Sysctls, m.HostNetworkMode, ipcModeIsHost(ipcMode)); err != nil {
		return err
	}
	if err := validateUlimits(m.Ulimits); err != nil {
		return err
	}
	if m.Cpus != nil && *m.Cpus <= 0 {
		return errors.New("cpus must be greater than 0")
	}
	if err := validateMemorySwap(m.MemoryLimit != nil && *m.MemoryLimit > 0, memorySwapWireValue(m.MemorySwap)); err != nil {
		return err
	}
	for i, d := range m.Devices {
		if err := validateDeviceMapping(d, i); err != nil {
			return err
		}
	}
	for i, t := range m.Tmpfs {
		if err := validateTmpfsMount(t, i); err != nil {
			return err
		}
	}
	if m.WorkingDir != nil {
		wd := strings.TrimSpace(*m.WorkingDir)
		if wd != "" && !path.IsAbs(wd) {
			return errors.New("workingDir must be an absolute container path")
		}
	}
	return nil
}

// memorySwapWireValue returns the stored swap pointer unchanged when it is -1,
// otherwise it is only used for "set vs unset" checks (units do not matter).
func memorySwapWireValue(swap *int64) *int64 {
	return swap
}
