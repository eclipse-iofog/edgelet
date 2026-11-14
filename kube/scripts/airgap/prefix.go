package airgap

import "strings"

// Resolve returns the image name kubelet and the staged manifests use.
// An empty registry leaves ref unchanged. A ref that is not on the pin list
// is returned unchanged, including an image an operator created.
func Resolve(registry, ref string) string {
	registry = cleanRegistry(registry)
	ref = strings.TrimSpace(ref)
	if registry == "" || !listed(ref) || strings.HasPrefix(ref, registry+"/") {
		return ref
	}
	return registry + "/" + ref
}

// Names returns the pin list as kubelet and the staged manifests use it.
func Names(registry string) []string {
	images := Images()
	out := make([]string, len(images))
	for i, image := range images {
		out[i] = Resolve(registry, image)
	}
	return out
}

// PrefixedNames are the extra names an imported pin-list image is tagged with.
// The tag without a digest is included so kubelet can use the pause name.
// An empty registry, or a ref that is not on the pin list, returns nil.
func PrefixedNames(registry, ref string) []string {
	registry = cleanRegistry(registry)
	ref = strings.TrimSpace(ref)
	if registry == "" || !listed(ref) {
		return nil
	}
	var out []string
	add := func(name string) {
		if name == "" || name == ref {
			return
		}
		for _, existing := range out {
			if existing == name {
				return
			}
		}
		out = append(out, name)
	}
	add(Resolve(registry, ref))
	add(Resolve(registry, referenceName(ref)))
	return out
}

func cleanRegistry(registry string) string {
	return strings.Trim(strings.TrimSpace(registry), "/")
}

func listed(ref string) bool {
	name := referenceName(ref)
	if name == "" {
		return false
	}
	for _, image := range Images() {
		if referenceName(image) == name {
			return true
		}
	}
	return false
}

func referenceName(ref string) string {
	name, _, _ := strings.Cut(strings.TrimSpace(ref), "@")
	return name
}
