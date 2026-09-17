package store

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

func TestControllerRuntimeClassReplaceAllAndUniqueName(t *testing.T) {
	db := openFreshStoreDB(t)

	first := &models.ControllerRuntimeClass{Name: "spin", Handler: "spin"}
	if err := db.SaveControllerRuntimeClasses([]*models.ControllerRuntimeClass{first}); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := db.LoadControllerRuntimeClasses()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 1 || got[0].Name != "spin" || got[0].Handler != "spin" {
		t.Fatalf("unexpected fleet runtime classes: %+v", got)
	}

	replacement := &models.ControllerRuntimeClass{Name: "nvidia", Handler: "nvidia"}
	if err := db.SaveControllerRuntimeClasses([]*models.ControllerRuntimeClass{replacement}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, err = db.LoadControllerRuntimeClasses()
	if err != nil {
		t.Fatalf("load after replace: %v", err)
	}
	if len(got) != 1 || got[0].Name != "nvidia" {
		t.Fatalf("expected replace-all, got %+v", got)
	}

	if err := db.SaveControllerRuntimeClasses(nil); err != nil {
		t.Fatalf("empty replace-all: %v", err)
	}
	got, err = db.LoadControllerRuntimeClasses()
	if err != nil {
		t.Fatalf("load after empty: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty fleet runtime classes, got %d", len(got))
	}

	dupA := &models.ControllerRuntimeClass{Name: "spin", Handler: "spin"}
	dupB := &models.ControllerRuntimeClass{Name: "SPIN", Handler: "wasm"}
	err = db.SaveControllerRuntimeClasses([]*models.ControllerRuntimeClass{dupA, dupB})
	if err == nil || !strings.Contains(err.Error(), "duplicate controller runtime class name") {
		t.Fatalf("expected unique name error, got %v", err)
	}
}

func TestLocalRuntimeClassDefaultAndInvalidSource(t *testing.T) {
	db := openFreshStoreDB(t)

	rc := &models.LocalRuntimeClass{Name: "spin", Handler: "spin"}
	if err := db.UpsertLocalRuntimeClass(rc); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if rc.Source != models.RuntimeClassSourceLocal {
		t.Fatalf("expected default source local on write, got %q", rc.Source)
	}
	got, err := db.GetLocalRuntimeClass("spin")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Source != models.RuntimeClassSourceLocal {
		t.Fatalf("expected default source local on read, got %q", got.Source)
	}

	managed := &models.LocalRuntimeClass{
		Name:    "nvidia",
		Handler: "nvidia",
		Source:  models.RuntimeClassSourceManaged,
	}
	if err := db.UpsertLocalRuntimeClass(managed); err != nil {
		t.Fatalf("upsert managed: %v", err)
	}
	got, err = db.GetLocalRuntimeClass("nvidia")
	if err != nil {
		t.Fatalf("get managed: %v", err)
	}
	if got.Source != models.RuntimeClassSourceManaged {
		t.Fatalf("expected managed source, got %q", got.Source)
	}

	invalid := &models.LocalRuntimeClass{Name: "bad", Handler: "bad", Source: "fleet"}
	err = db.UpsertLocalRuntimeClass(invalid)
	if err == nil || !strings.Contains(err.Error(), "invalid runtime class source") {
		t.Fatalf("expected invalid source error, got %v", err)
	}
}

func TestListLocalRuntimeClassesSortedByName(t *testing.T) {
	db := openFreshStoreDB(t)
	for _, name := range []string{"zeta", "alpha", "mid"} {
		rc := &models.LocalRuntimeClass{Name: name, Handler: name}
		if err := db.UpsertLocalRuntimeClass(rc); err != nil {
			t.Fatalf("upsert %s: %v", name, err)
		}
	}
	items, err := db.ListLocalRuntimeClasses()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(items))
	}
	want := []string{"alpha", "mid", "zeta"}
	for i, name := range want {
		if items[i].Name != name {
			t.Fatalf("expected sorted names %v, got %s at %d", want, items[i].Name, i)
		}
	}
}

func TestIsManagedFleetRuntimeClass(t *testing.T) {
	db := openFreshStoreDB(t)
	if err := db.SaveControllerRuntimeClasses([]*models.ControllerRuntimeClass{
		{Name: "spin", Handler: "spin"},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	managed, err := db.IsManagedFleetRuntimeClass("spin", true)
	if err != nil || !managed {
		t.Fatalf("expected managed while provisioned, got %v err=%v", managed, err)
	}
	managed, err = db.IsManagedFleetRuntimeClass("SPIN", true)
	if err != nil || !managed {
		t.Fatalf("expected case-insensitive managed name, got %v err=%v", managed, err)
	}
	managed, err = db.IsManagedFleetRuntimeClass("spin", false)
	if err != nil || managed {
		t.Fatalf("expected not managed when unprovisioned, got %v err=%v", managed, err)
	}
	managed, err = db.IsManagedFleetRuntimeClass("local-only", true)
	if err != nil || managed {
		t.Fatalf("expected unknown name not managed, got %v err=%v", managed, err)
	}

	got, err := db.GetControllerRuntimeClass("spin")
	if err != nil || got.Handler != "spin" {
		t.Fatalf("get fleet row: %+v err=%v", got, err)
	}
	if _, err := db.GetControllerRuntimeClass("missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected ErrNoRows, got %v", err)
	}
}
