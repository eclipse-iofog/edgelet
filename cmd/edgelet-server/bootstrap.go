//go:build linux && cgo

package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/constants"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/eclipse-iofog/edgelet/pkg/containerd"
	"github.com/eclipse-iofog/edgelet/pkg/data"
)

const (
	containerdBootstrapMaxAttempts = 3
	containerdBootstrapBaseBackoff = 2 * time.Second
	containerdBootstrapMaxBackoff  = 10 * time.Second
)

var (
	reapManagedShimsBeforeBootstrapCleanup = containerd.ReapManagedShimsUntilClear
	prepareEmbeddedContainerdBootstrapFn   = containerd.PrepareEmbeddedContainerdBootstrap
)

type containerdStarter interface {
	Start() error
	Stop()
}

type bootstrapDeps struct {
	ensureDependencies func() error
	newService         func() containerdStarter
	cleanupRuntime     func() error
	prepareBootstrap   func()
	sleep              func(time.Duration)
}

func startEmbeddedContainerdWithRetry() (*containerd.Service, error) {
	if err := containerd.UseWasmShimLogDir(); err != nil {
		return nil, fmt.Errorf("prepare wasm shim log: %w", err)
	}
	deps := bootstrapDeps{
		ensureDependencies: data.EnsureExtracted,
		newService: func() containerdStarter {
			return containerd.NewService()
		},
		cleanupRuntime: containerd.CleanupRuntimeArtifacts,
		sleep:          time.Sleep,
	}

	svc, err := startEmbeddedContainerdWithRetryDeps(deps)
	if err != nil {
		return nil, err
	}

	typed, ok := svc.(*containerd.Service)
	if !ok {
		return nil, fmt.Errorf("unexpected embedded containerd service type %T", svc)
	}
	return typed, nil
}

func startEmbeddedContainerdWithRetryDeps(deps bootstrapDeps) (containerdStarter, error) {
	var lastErr error
	backoff := containerdBootstrapBaseBackoff
	prepare := deps.prepareBootstrap
	if prepare == nil {
		prepare = prepareEmbeddedContainerdBootstrapFn
	}

	for attempt := 1; attempt <= containerdBootstrapMaxAttempts; attempt++ {
		prepare()

		if err := deps.ensureDependencies(); err != nil {
			lastErr = wrapBootstrapErr("prepare embedded runtime bundle", err)
		} else {
			svc := deps.newService()
			if err := svc.Start(); err == nil {
				return svc, nil
			}
			lastErr = wrapBootstrapContainerdStartErr(err)
			svc.Stop()
		}

		logging.LogWarn("MAIN_DAEMON", fmt.Sprintf(
			"Embedded containerd startup attempt %d/%d failed: %v",
			attempt, containerdBootstrapMaxAttempts, lastErr,
		))

		if attempt == containerdBootstrapMaxAttempts {
			break
		}

		if err := reapManagedShimsBeforeBootstrapCleanup(constants.EdgeletContainerdSocket, containerd.DefaultShimReapBudgetCap); err != nil {
			logging.LogWarn("MAIN_DAEMON", fmt.Sprintf("deferring runtime artifact cleanup after incomplete shim reap: %v", err))
		} else if err := deps.cleanupRuntime(); err != nil {
			logging.LogWarn("MAIN_DAEMON", fmt.Sprintf("Embedded containerd runtime cleanup failed before retry: %v", err))
		}

		deps.sleep(backoff)
		if backoff < containerdBootstrapMaxBackoff {
			backoff *= 2
			if backoff > containerdBootstrapMaxBackoff {
				backoff = containerdBootstrapMaxBackoff
			}
		}
	}

	if lastErr == nil {
		lastErr = errors.New("embedded containerd startup failed with no recorded error")
	}
	return nil, fmt.Errorf("embedded containerd did not become ready after %d attempts: %w", containerdBootstrapMaxAttempts, lastErr)
}

func wrapBootstrapErr(stage string, err error) error {
	if err == nil {
		return fmt.Errorf("%s failed with no error detail", stage)
	}
	return fmt.Errorf("%s: %w", stage, err)
}

func wrapBootstrapContainerdStartErr(err error) error {
	if err == nil {
		return fmt.Errorf("embedded containerd exited before ready: %w", containerd.ErrContainerdExitedEarly)
	}
	switch {
	case errors.Is(err, containerd.ErrContainerdSpawnFailure):
		return fmt.Errorf("embedded containerd child spawn failed: %w", err)
	case errors.Is(err, containerd.ErrContainerdReadiness):
		return fmt.Errorf("embedded containerd readiness check failed: %w", err)
	case errors.Is(err, containerd.ErrContainerdExitedEarly):
		return fmt.Errorf("embedded containerd exited before ready: %w", err)
	default:
		return fmt.Errorf("embedded containerd startup failed: %w", err)
	}
}
