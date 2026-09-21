package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

// v1Tables is the full application schema after 001_edgelet_schema_v1.sql (fresh DB).
var v1Tables = []string{
	"schema_versions",
	"controller_microservices",
	"controller_registries",
	"controller_volume_mounts",
	"runtime_container_refs",
	"agent_edgeguard_signature",
	"agent_credentials",
	"local_service_account_tokens",
	"local_workloads",
	"local_registries",
	"local_runtime_classes",
	"system_control_plane",
}

// legacyTables must not exist after Plan 13 wipe-only schema lock.
var legacyTables = []string{
	"microservices",
	"registries",
	"volume_mounts",
	"container_state",
	"local_container_state",
	"local_deployed_microservices",
	"control_plane_deployments",
	"service_account_tokens",
	"edgeguard_signature",
}

func openFreshStoreDB(t *testing.T) *DB {
	t.Helper()
	db := GetInstance()
	if err := db.Open(t.TempDir()); err != nil {
		t.Fatalf("open sqlite DB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func tableExists(t *testing.T, db *DB, name string) bool {
	t.Helper()
	var got string
	err := db.Conn().QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name,
	).Scan(&got)
	return err == nil && got == name
}

func tableColumns(t *testing.T, db *DB, table string) map[string]struct {
	ctype string
	pk    int
} {
	t.Helper()
	rows, err := db.Conn().Query(`PRAGMA table_info(` + table + `)`) //nolint:gosec // fixed test table names
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer func() {
		_ = rows.Close()
	}()

	out := make(map[string]struct {
		ctype string
		pk    int
	})
	for rows.Next() {
		var (
			cid       int
			name      string
			ctype     string
			notnull   int
			dfltValue any
			pk        int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan pragma row for %s: %v", table, err)
		}
		out[name] = struct {
			ctype string
			pk    int
		}{ctype: ctype, pk: pk}
	}
	return out
}

func assertHasColumns(t *testing.T, table string, cols map[string]struct {
	ctype string
	pk    int
}, required []string) {
	t.Helper()
	for _, col := range required {
		if _, ok := cols[col]; !ok {
			t.Fatalf("table %s: expected column %q", table, col)
		}
	}
}

func assertAbsentColumns(t *testing.T, table string, cols map[string]struct {
	ctype string
	pk    int
}, absent []string) {
	t.Helper()
	for _, col := range absent {
		if _, ok := cols[col]; ok {
			t.Fatalf("table %s: legacy column %q must not exist", table, col)
		}
	}
}

func TestSchemaV1_AllTablesExist(t *testing.T) {
	db := openFreshStoreDB(t)
	for _, table := range v1Tables {
		if !tableExists(t, db, table) {
			t.Fatalf("missing v1 table %q", table)
		}
	}
}

func TestSchemaV1_NoLegacyTables(t *testing.T) {
	db := openFreshStoreDB(t)
	for _, table := range legacyTables {
		if tableExists(t, db, table) {
			t.Fatalf("legacy table %q must not exist after v1 migration", table)
		}
	}
}

func TestSchemaV1_SchemaVersionOne(t *testing.T) {
	db := openFreshStoreDB(t)

	var version int
	var description string
	err := db.Conn().QueryRow(
		`SELECT version, description FROM schema_versions ORDER BY version DESC LIMIT 1`,
	).Scan(&version, &description)
	if err != nil {
		t.Fatalf("schema_versions row: %v", err)
	}
	if version != 4 {
		t.Fatalf("expected latest schema version 4, got %d", version)
	}
	if description != "004_edgelet_schema_v4.sql" {
		t.Fatalf("unexpected migration description: %q", description)
	}

	var v1Desc string
	if err := db.Conn().QueryRow(`SELECT description FROM schema_versions WHERE version = 1`).Scan(&v1Desc); err != nil {
		t.Fatalf("schema v1 row: %v", err)
	}
	if v1Desc != "001_edgelet_schema_v1.sql" {
		t.Fatalf("unexpected v1 migration description: %q", v1Desc)
	}

	var maxVersion int
	if err := db.Conn().QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_versions`).Scan(&maxVersion); err != nil {
		t.Fatalf("max schema version: %v", err)
	}
	if maxVersion != 4 {
		t.Fatalf("expected max schema version 4, got %d", maxVersion)
	}
}

func TestSchemaV1_ReopenKeepsVersionOne(t *testing.T) {
	dbDir := t.TempDir()
	db := GetInstance()
	if err := db.Open(dbDir); err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := db.Open(dbDir); err != nil {
		t.Fatalf("second open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if !tableExists(t, db, "local_workloads") {
		t.Fatal("expected local_workloads after reopen")
	}

	dbPath := filepath.Join(dbDir, dbFileName)
	if dbPath == "" {
		t.Fatal("expected sqlite db path")
	}

	var maxVersion int
	if err := db.Conn().QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_versions`).Scan(&maxVersion); err != nil {
		t.Fatalf("schema version after reopen: %v", err)
	}
	if maxVersion != 4 {
		t.Fatalf("expected max schema version 4 after reopen, got %d", maxVersion)
	}
}

func TestSchemaV1_LocalWorkloadsKeyColumns(t *testing.T) {
	db := openFreshStoreDB(t)
	cols := tableColumns(t, db, "local_workloads")

	assertHasColumns(t, "local_workloads", cols, []string{
		"local_uuid",
		"application_name",
		"microservice_name",
		"manifest_yaml",
		"desired_state",
		"runtime_state",
		"last_error",
		"restart_count",
		"last_transition_at",
		"last_reconcile_at",
		"last_start_attempt_at",
		"failure_count",
		"deleted_at",
		"generation",
		"observed_generation",
	})

	var indexName string
	err := db.Conn().QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type='index' AND name='idx_local_workloads_unique_app_name'`).Scan(&indexName)
	if err != nil {
		t.Fatalf("expected unique app/name index on local_workloads: %v", err)
	}
}

func TestSchemaV1_SystemControlPlaneKeyColumns(t *testing.T) {
	db := openFreshStoreDB(t)
	cols := tableColumns(t, db, "system_control_plane")

	assertHasColumns(t, "system_control_plane", cols, []string{
		"id",
		"controller_uuid",
		"namespace",
		"name",
		"manifest_yaml",
		"image",
		"container_id",
		"desired_state",
		"runtime_state",
		"last_error",
		"restart_count",
		"last_transition_at",
		"last_reconcile_at",
		"last_start_attempt_at",
		"failure_count",
		"deleted_at",
		"generation",
		"observed_generation",
		"controller_registered",
		"initial_rebuild_skipped",
	})
	if cols["id"].pk != 1 {
		t.Fatal("system_control_plane.id must be primary key")
	}
}

func TestSchemaV1_RuntimeContainerRefsKeyColumns(t *testing.T) {
	db := openFreshStoreDB(t)
	cols := tableColumns(t, db, "runtime_container_refs")

	assertHasColumns(t, "runtime_container_refs", cols, []string{
		"ms_uuid",
		"scope",
		"workload_id",
		"sandbox_id",
		"updated_at",
	})
	if cols["ms_uuid"].pk != 1 || cols["scope"].pk != 2 {
		t.Fatalf("expected composite PK (ms_uuid, scope), got pk flags ms_uuid=%d scope=%d",
			cols["ms_uuid"].pk, cols["scope"].pk)
	}

	var indexName string
	err := db.Conn().QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type='index' AND name='idx_runtime_container_refs_workload'`).Scan(&indexName)
	if err != nil {
		t.Fatalf("expected workload_id index on runtime_container_refs: %v", err)
	}
}

func TestSchemaV1_LocalRuntimeClassesKeyColumns(t *testing.T) {
	db := openFreshStoreDB(t)
	cols := tableColumns(t, db, "local_runtime_classes")

	assertHasColumns(t, "local_runtime_classes", cols, []string{
		"name",
		"handler",
		"source",
		"created_at",
		"updated_at",
	})
	assertAbsentColumns(t, "local_runtime_classes", cols, []string{
		"binary_path",
		"runtime_name",
		"runtime_local_name",
	})
}

func TestServiceAccountTokenCRUD(t *testing.T) {
	db := openFreshStoreDB(t)

	token := &models.ServiceAccountToken{
		ID:                 "tok-1",
		TokenUse:           "serviceaccount",
		PrincipalType:      "serviceaccount",
		Subject:            "system:serviceaccount:app:ms",
		MicroserviceUUID:   "ms-uuid",
		ApplicationName:    "app",
		ServiceAccountName: "sa-1",
		RoleRefKind:        "Role",
		RoleRefName:        "role-1",
		RBACVersion:        "v1",
		RulesByGroupJSON:   `{"edgelet.iofog.org/v1":[{"resources":["logs"],"verbs":["get"]}]}`,
		ClaimsJSON:         `{}`,
		Issuer:             "https://edgelet.default.svc.bridge.local",
		Audience:           "https://edgelet.default.svc.bridge.local",
		Alg:                "EdDSA",
		JTI:                "jti-1",
		TokenSHA256:        "sha256-1",
		IssuedAt:           time.Now().Unix(),
		NotBefore:          time.Now().Unix(),
		ExpiresAt:          time.Now().Add(time.Hour).Unix(),
	}
	if err := db.UpsertServiceAccountToken(token); err != nil {
		t.Fatalf("upsert token: %v", err)
	}

	items, err := db.ListServiceAccountTokens()
	if err != nil {
		t.Fatalf("list tokens: %v", err)
	}
	if len(items) != 1 || items[0].JTI != "jti-1" {
		t.Fatalf("expected 1 token row with jti-1, got %+v", items)
	}

	if err := db.RevokeServiceAccountToken("jti-1", time.Now().Unix()); err != nil {
		t.Fatalf("revoke token: %v", err)
	}
	items, err = db.ListServiceAccountTokens()
	if err != nil {
		t.Fatalf("list after revoke: %v", err)
	}
	if items[0].RevokedAt == nil {
		t.Fatal("expected revoked_at after revocation")
	}
}

func TestLocalWorkloadCRUD(t *testing.T) {
	db := openFreshStoreDB(t)

	ms := &models.LocalDeployedMicroservice{
		LocalUUID:        "local-1",
		ApplicationName:  "",
		MicroserviceName: "edge-processor",
		SourceName:       "local-apply",
		ManifestYAML:     "kind: Microservice",
		ImageName:        "nginx:latest",
		State:            "running",
		ContainerID:      "cid-1",
	}
	if err := db.UpsertLocalWorkload(ms); err != nil {
		t.Fatalf("upsert local workload: %v", err)
	}

	list, err := db.ListLocalWorkloads()
	if err != nil {
		t.Fatalf("list local workloads: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 local workload row, got %d", len(list))
	}

	got, err := db.GetLocalWorkload("local-1")
	if err != nil {
		t.Fatalf("get local workload: %v", err)
	}
	if got.ApplicationName != "edgelet" {
		t.Fatalf("expected application_name edgelet, got %q", got.ApplicationName)
	}
	if got.DesiredState != "running" || got.RuntimeState != "running" {
		t.Fatalf("expected running states, got desired=%q runtime=%q", got.DesiredState, got.RuntimeState)
	}
	if got.Generation != 1 {
		t.Fatalf("expected generation=1, got %d", got.Generation)
	}
	if got.LastReconcileAt != 0 || got.LastStartAttemptAt != 0 || got.FailureCount != 0 {
		t.Fatalf("expected reconcile defaults zero, got reconcile=%d start=%d failures=%d",
			got.LastReconcileAt, got.LastStartAttemptAt, got.FailureCount)
	}
	if got.LastTransitionAt <= 0 {
		t.Fatalf("expected last_transition_at > 0, got %d", got.LastTransitionAt)
	}

	if err := db.DeleteLocalWorkload("local-1"); err != nil {
		t.Fatalf("delete local workload: %v", err)
	}
	if _, err := db.GetLocalWorkload("local-1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected ErrNoRows after delete, got %v", err)
	}
}

func TestLocalWorkloadUniqueByAppName(t *testing.T) {
	db := openFreshStoreDB(t)

	first := &models.LocalDeployedMicroservice{
		LocalUUID:        "local-a",
		ApplicationName:  "edgelet",
		MicroserviceName: "router",
		SourceName:       "local-apply",
		ManifestYAML:     "kind: Microservice",
		ImageName:        "nginx:latest",
		State:            "running",
	}
	if err := db.UpsertLocalWorkload(first); err != nil {
		t.Fatalf("insert first local workload: %v", err)
	}

	second := &models.LocalDeployedMicroservice{
		LocalUUID:        "local-b",
		ApplicationName:  "EDGELET",
		MicroserviceName: "router",
		SourceName:       "local-apply",
		ManifestYAML:     "kind: Microservice",
		ImageName:        "nginx:latest",
		State:            "running",
	}
	if err := db.UpsertLocalWorkload(second); err == nil {
		t.Fatal("expected unique constraint violation for duplicate local app/name")
	}
}

func TestRuntimeContainerRefScopedCRUD(t *testing.T) {
	db := openFreshStoreDB(t)

	if err := db.UpsertRuntimeContainerRef("ms-ctrl", RuntimeScopeController, "wl-1", "sb-1"); err != nil {
		t.Fatalf("upsert controller ref: %v", err)
	}
	if err := db.UpsertRuntimeContainerRef("ms-local", RuntimeScopeLocal, "wl-2", "sb-2"); err != nil {
		t.Fatalf("upsert local ref: %v", err)
	}

	ctrl, err := db.GetRuntimeContainerRef("ms-ctrl", RuntimeScopeController)
	if err != nil || ctrl == nil || ctrl.WorkloadID != "wl-1" {
		t.Fatalf("controller ref: %+v err=%v", ctrl, err)
	}
	local, err := db.GetRuntimeContainerRef("ms-local", RuntimeScopeLocal)
	if err != nil || local == nil || local.WorkloadID != "wl-2" {
		t.Fatalf("local ref: %+v err=%v", local, err)
	}

	if err := db.DeleteRuntimeContainerRef("ms-ctrl", RuntimeScopeController); err != nil {
		t.Fatalf("delete controller ref: %v", err)
	}
	ctrl, _ = db.GetRuntimeContainerRef("ms-ctrl", RuntimeScopeController)
	if ctrl != nil {
		t.Fatal("expected controller ref deleted")
	}

	if err := db.ClearRuntimeContainerRefs(RuntimeScopeLocal); err != nil {
		t.Fatalf("clear local refs: %v", err)
	}
	local, _ = db.GetRuntimeContainerRef("ms-local", RuntimeScopeLocal)
	if local != nil {
		t.Fatal("expected local refs cleared")
	}
}

func TestLocalRuntimeClassCRUD(t *testing.T) {
	db := openFreshStoreDB(t)

	rc := &models.LocalRuntimeClass{
		Name:        "edgelet-wasmtime",
		Handler:     "edgelet-wasmtime",
		RuntimeName: "edgelet-wasmtime",
	}
	if err := db.UpsertLocalRuntimeClass(rc); err != nil {
		t.Fatalf("upsert local runtime class: %v", err)
	}

	items, err := db.ListLocalRuntimeClasses()
	if err != nil {
		t.Fatalf("list local runtime classes: %v", err)
	}
	if len(items) != 1 || items[0].RuntimeName != "edgelet-wasmtime" {
		t.Fatalf("expected 1 edgelet-wasmtime runtime class, got %+v", items)
	}

	got, err := db.GetLocalRuntimeClass("edgelet-wasmtime")
	if err != nil || got.Handler != "edgelet-wasmtime" {
		t.Fatalf("get runtime class: %+v err=%v", got, err)
	}

	if err := db.DeleteLocalRuntimeClass("edgelet-wasmtime"); err != nil {
		t.Fatalf("delete runtime class: %v", err)
	}
	if _, err := db.GetLocalRuntimeClass("edgelet-wasmtime"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected ErrNoRows after delete, got %v", err)
	}
}
