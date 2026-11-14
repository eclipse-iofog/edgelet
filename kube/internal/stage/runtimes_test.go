package stage

import (
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet-kube/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRuntimeClassesFollowRegisteredHandlers(t *testing.T) {
	registered := []string{"crun", "nvidia", "edgelet-wasmtime", "nvidia-cdi"}
	known := []string{"crun", "nvidia", "edgelet-wasmtime", "nvidia-cdi", "wasmer", "runc", "spin"}
	docs, err := runtimeClassManifests(true, registered, known)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 {
		t.Fatalf("manifests %d", len(docs))
	}
	var applied, removed string
	for _, doc := range docs {
		if doc.name != runtimeAddon {
			t.Fatalf("addon %s", doc.name)
		}
		if doc.enabled {
			applied = string(doc.body)
			continue
		}
		removed = string(doc.body)
	}
	objs, err := decode([]byte(applied))
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != len(registered) {
		t.Fatalf("applied %#v", objs)
	}
	for _, name := range registered {
		obj := find(t, objs, "RuntimeClass", name)
		if obj.GetAPIVersion() != "node.k8s.io/v1" {
			t.Fatalf("%s version %s", name, obj.GetAPIVersion())
		}
		if nestedString(t, obj, "handler") != name {
			t.Fatalf("%s handler %#v", name, obj.Object)
		}
		if _, found, err := unstructured.NestedFieldNoCopy(obj.Object, "scheduling"); err != nil || found {
			t.Fatalf("%s sets scheduling", name)
		}
	}
	for _, absent := range []string{"wasmer", "runc", "spin"} {
		if strings.Contains(applied, "name: "+absent) {
			t.Fatalf("applied missing handler %s", absent)
		}
		if !strings.Contains(removed, "name: "+absent) {
			t.Fatalf("removed body missing %s", absent)
		}
	}
}

func TestDisableRuntimesDeletesEveryClass(t *testing.T) {
	docs, err := runtimeClassManifests(false, []string{"crun", "nvidia"}, []string{"crun", "nvidia", "wasmer"})
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || docs[0].enabled {
		t.Fatalf("manifests %#v", docs)
	}
	objs, err := decode(docs[0].body)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, obj := range objs {
		got[obj.GetName()] = true
	}
	for _, name := range []string{"crun", "nvidia", "wasmer"} {
		if !got[name] {
			t.Fatalf("missing %s in %#v", name, got)
		}
	}
}

func TestOtherDisableLeavesRuntimeClasses(t *testing.T) {
	srv := config.Defaults()
	srv.Disable = []string{"traefik", "coredns"}
	docs, err := runtimeManifests(srv)
	if err != nil {
		t.Fatal(err)
	}
	var enabled bool
	for _, doc := range docs {
		if doc.enabled {
			enabled = true
		}
	}
	if !enabled {
		t.Fatal("an unrelated disable dropped the runtime classes")
	}
}

func TestRuntimeClassRejectsBadHandler(t *testing.T) {
	if _, err := runtimeClassManifests(true, []string{"NVIDIA"}, []string{"NVIDIA"}); err == nil {
		t.Fatal("expected an invalid handler to fail")
	}
}
