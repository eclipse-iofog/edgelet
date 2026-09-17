package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

// SaveControllerMicroservices replaces all controller microservice rows in a single transaction.
func (d *DB) SaveControllerMicroservices(microservices []*models.Microservice) error {
	tx, err := d.Conn().Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec("DELETE FROM controller_microservices"); err != nil {
		return fmt.Errorf("failed to clear controller_microservices: %w", err)
	}

	for _, ms := range microservices {
		if err := insertMicroservice(tx, ms); err != nil {
			return fmt.Errorf("failed to insert microservice %s: %w", ms.MicroserviceUUID, err)
		}
	}

	return tx.Commit()
}

// LoadControllerMicroservices retrieves all controller microservices ordered by uuid.
func (d *DB) LoadControllerMicroservices() ([]*models.Microservice, error) {
	rows, err := d.Conn().Query(`SELECT
		uuid, image_name, container_id, registry_id,
		rebuild, host_network_mode, is_privileged, log_size,
		is_router, microservice_name, application_name,
		is_nats, schedule, delete_flag, delete_with_cleanup,
		is_stuck_in_restart, is_updating,
		config, run_as_user, platform, runtime, container_ip,
		annotations, pid_mode, ipc_mode, cpu_set_cpus, memory_limit,
		port_mappings, volume_mappings, env_vars, args,
		cdi_devs, cap_add, cap_drop, extra_hosts, healthcheck,
		models, sysctls, ulimits, devices, tmpfs,
		entrypoint, commands, run_as_group, read_only_root_filesystem,
		cpus, memory_reservation, memory_swap, shm_size, working_dir
	FROM controller_microservices ORDER BY uuid`)
	if err != nil {
		return nil, fmt.Errorf("failed to query controller_microservices: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	var result []*models.Microservice
	for rows.Next() {
		ms, err := scanMicroservice(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan microservice: %w", err)
		}
		result = append(result, ms)
	}
	if result == nil {
		result = make([]*models.Microservice, 0)
	}
	return result, rows.Err()
}

// ClearControllerMicroservices removes all controller microservice rows (used on deprovision).
func (d *DB) ClearControllerMicroservices() error {
	_, err := d.Conn().Exec("DELETE FROM controller_microservices")
	return err
}

func insertMicroservice(tx *sql.Tx, ms *models.Microservice) error {
	portMappingsJSON, _ := json.Marshal(ms.PortMappings)
	volumeMappingsJSON, _ := json.Marshal(ms.VolumeMappings)
	envVarsJSON, _ := json.Marshal(ms.EnvVars)
	argsJSON, _ := json.Marshal(ms.Args)
	cdiDevsJSON, _ := json.Marshal(ms.CdiDevs)
	capAddJSON, _ := json.Marshal(ms.CapAdd)
	capDropJSON, _ := json.Marshal(ms.CapDrop)
	extraHostsJSON, _ := json.Marshal(ms.ExtraHosts)

	var healthcheckJSON *string
	if ms.Healthcheck != nil {
		b, _ := json.Marshal(ms.Healthcheck)
		s := string(b)
		healthcheckJSON = &s
	}

	modelsJSON, err := ms.MarshalModelsJSON()
	if err != nil {
		return fmt.Errorf("marshal models: %w", err)
	}
	sysctlsJSON, err := marshalJSONDefault(ms.Sysctls, "{}")
	if err != nil {
		return fmt.Errorf("marshal sysctls: %w", err)
	}
	ulimitsJSON, err := marshalJSONDefault(ms.Ulimits, "{}")
	if err != nil {
		return fmt.Errorf("marshal ulimits: %w", err)
	}
	devicesJSON, err := marshalJSONDefault(ms.Devices, "[]")
	if err != nil {
		return fmt.Errorf("marshal devices: %w", err)
	}
	tmpfsJSON, err := marshalJSONDefault(ms.Tmpfs, "[]")
	if err != nil {
		return fmt.Errorf("marshal tmpfs: %w", err)
	}

	_, err = tx.Exec(`INSERT OR REPLACE INTO controller_microservices (
		uuid, image_name, container_id, registry_id,
		rebuild, host_network_mode, is_privileged, log_size,
		is_router, microservice_name, application_name,
		is_nats, schedule, delete_flag, delete_with_cleanup,
		is_stuck_in_restart, is_updating,
		config, run_as_user, platform, runtime, container_ip,
		annotations, pid_mode, ipc_mode, cpu_set_cpus, memory_limit,
		port_mappings, volume_mappings, env_vars, args,
		cdi_devs, cap_add, cap_drop, extra_hosts, healthcheck,
		models, sysctls, ulimits, devices, tmpfs,
		entrypoint, commands, run_as_group, read_only_root_filesystem,
		cpus, memory_reservation, memory_swap, shm_size, working_dir,
		updated_at
	) VALUES (
		?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?
	)`,
		ms.MicroserviceUUID, ms.ImageName, ms.ContainerID, ms.RegistryID,
		boolToInt(ms.Rebuild), boolToInt(ms.HostNetworkMode), boolToInt(ms.IsPrivileged), ms.LogSize,
		boolToInt(ms.IsRouter), ms.MicroserviceName, ms.ApplicationName,
		boolToInt(ms.IsNats), ms.Schedule, boolToInt(ms.Delete), boolToInt(ms.DeleteWithCleanup),
		boolToInt(ms.IsStuckInRestart), boolToInt(ms.GetIsUpdating()),
		ms.Config, ms.RunAsUser, ms.Platform, ms.Runtime, ms.ContainerIPAddress,
		ms.Annotations, ms.PidMode, ms.IpcMode, ms.CPUSetCpus, ms.MemoryLimit,
		string(portMappingsJSON), string(volumeMappingsJSON), string(envVarsJSON), string(argsJSON),
		string(cdiDevsJSON), string(capAddJSON), string(capDropJSON), string(extraHostsJSON),
		healthcheckJSON,
		modelsJSON, sysctlsJSON, ulimitsJSON, devicesJSON, tmpfsJSON,
		encodeOptionalArgv(ms.Entrypoint), encodeOptionalArgv(ms.Commands), ms.RunAsGroup,
		boolToInt(ms.ReadOnlyRootFilesystem),
		ms.Cpus, ms.MemoryReservation, ms.MemorySwap, ms.ShmSize, ms.WorkingDir,
		time.Now().Unix(),
	)
	return err
}

func scanMicroservice(rows *sql.Rows) (*models.Microservice, error) {
	ms := &models.Microservice{}

	var (
		rebuild, hostNetworkMode, isPrivileged, isRouter  int
		isNats, deleteFlag, deleteWithCleanup             int
		isStuckInRestart, isUpdating                      int
		portMappingsJSON, volumeMappingsJSON, envVarsJSON string
		argsJSON, cdiDevsJSON, capAddJSON, capDropJSON    string
		extraHostsJSON                                    string
		healthcheckJSON                                   *string
		modelsJSON, sysctlsJSON, ulimitsJSON              string
		devicesJSON, tmpfsJSON                            string
		entrypointJSON, commandsJSON                      *string
		readOnlyRoot                                      int
	)

	err := rows.Scan(
		&ms.MicroserviceUUID, &ms.ImageName, &ms.ContainerID, &ms.RegistryID,
		&rebuild, &hostNetworkMode, &isPrivileged, &ms.LogSize,
		&isRouter, &ms.MicroserviceName, &ms.ApplicationName,
		&isNats, &ms.Schedule, &deleteFlag, &deleteWithCleanup,
		&isStuckInRestart, &isUpdating,
		&ms.Config, &ms.RunAsUser, &ms.Platform, &ms.Runtime, &ms.ContainerIPAddress,
		&ms.Annotations, &ms.PidMode, &ms.IpcMode, &ms.CPUSetCpus, &ms.MemoryLimit,
		&portMappingsJSON, &volumeMappingsJSON, &envVarsJSON, &argsJSON,
		&cdiDevsJSON, &capAddJSON, &capDropJSON, &extraHostsJSON, &healthcheckJSON,
		&modelsJSON, &sysctlsJSON, &ulimitsJSON, &devicesJSON, &tmpfsJSON,
		&entrypointJSON, &commandsJSON, &ms.RunAsGroup, &readOnlyRoot,
		&ms.Cpus, &ms.MemoryReservation, &ms.MemorySwap, &ms.ShmSize, &ms.WorkingDir,
	)
	if err != nil {
		return nil, err
	}

	ms.Rebuild = intToBool(rebuild)
	ms.HostNetworkMode = intToBool(hostNetworkMode)
	ms.IsPrivileged = intToBool(isPrivileged)
	ms.IsRouter = intToBool(isRouter)
	ms.IsNats = intToBool(isNats)
	ms.Delete = intToBool(deleteFlag)
	ms.DeleteWithCleanup = intToBool(deleteWithCleanup)
	ms.IsStuckInRestart = intToBool(isStuckInRestart)
	ms.ReadOnlyRootFilesystem = intToBool(readOnlyRoot)
	if intToBool(isUpdating) {
		ms.SetIsUpdating(true)
	}

	_ = json.Unmarshal([]byte(portMappingsJSON), &ms.PortMappings)
	_ = json.Unmarshal([]byte(volumeMappingsJSON), &ms.VolumeMappings)
	_ = json.Unmarshal([]byte(envVarsJSON), &ms.EnvVars)
	_ = json.Unmarshal([]byte(argsJSON), &ms.Args)
	_ = json.Unmarshal([]byte(cdiDevsJSON), &ms.CdiDevs)
	_ = json.Unmarshal([]byte(capAddJSON), &ms.CapAdd)
	_ = json.Unmarshal([]byte(capDropJSON), &ms.CapDrop)
	_ = json.Unmarshal([]byte(extraHostsJSON), &ms.ExtraHosts)

	if healthcheckJSON != nil {
		ms.Healthcheck = &models.Healthcheck{}
		_ = json.Unmarshal([]byte(*healthcheckJSON), ms.Healthcheck)
	}
	_ = ms.UnmarshalModelsJSON(modelsJSON)
	_ = json.Unmarshal([]byte(sysctlsJSON), &ms.Sysctls)
	_ = json.Unmarshal([]byte(ulimitsJSON), &ms.Ulimits)
	_ = json.Unmarshal([]byte(devicesJSON), &ms.Devices)
	_ = json.Unmarshal([]byte(tmpfsJSON), &ms.Tmpfs)
	ms.Entrypoint = decodeOptionalArgv(entrypointJSON)
	ms.Commands = decodeOptionalArgv(commandsJSON)

	// Ensure nil slices become empty slices (matches NewMicroservice behavior)
	if ms.PortMappings == nil {
		ms.PortMappings = make([]*models.PortMapping, 0)
	}
	if ms.VolumeMappings == nil {
		ms.VolumeMappings = make([]*models.VolumeMapping, 0)
	}
	if ms.EnvVars == nil {
		ms.EnvVars = make([]*models.EnvVar, 0)
	}
	if ms.Args == nil {
		ms.Args = make([]string, 0)
	}
	if ms.CdiDevs == nil {
		ms.CdiDevs = make([]string, 0)
	}
	if ms.CapAdd == nil {
		ms.CapAdd = make([]string, 0)
	}
	if ms.CapDrop == nil {
		ms.CapDrop = make([]string, 0)
	}
	if ms.ExtraHosts == nil {
		ms.ExtraHosts = make([]string, 0)
	}

	return ms, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func intToBool(i int) bool {
	return i != 0
}

func marshalJSONDefault(v any, empty string) (string, error) {
	if v == nil {
		return empty, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if string(raw) == "null" {
		return empty, nil
	}
	return string(raw), nil
}

func encodeOptionalArgv(argv *[]string) *string {
	if argv == nil {
		return nil
	}
	raw, err := json.Marshal(*argv)
	if err != nil {
		empty := "[]"
		return &empty
	}
	s := string(raw)
	return &s
}

func decodeOptionalArgv(raw *string) *[]string {
	if raw == nil {
		return nil
	}
	var items []string
	if err := json.Unmarshal([]byte(*raw), &items); err != nil || items == nil {
		empty := []string{}
		return &empty
	}
	return &items
}
