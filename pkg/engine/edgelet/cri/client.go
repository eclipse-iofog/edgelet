//go:build linux

// Package cri provides a CRI client wrapper for the iofog engine.
// It connects to containerd's CRI plugin (same socket as main API) and exposes
// RunPodSandbox, CreateContainer, StartContainer, StopContainer, RemoveContainer,
// StopPodSandbox, RemovePodSandbox for proper CNI lifecycle management.
package cri

import (
	"context"
	"fmt"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/cgroups"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

// Client wraps the CRI RuntimeService gRPC client.
type Client struct {
	runtime runtimeapi.RuntimeServiceClient
	conn    *grpc.ClientConn
}

// NewClient dials the containerd socket and creates a CRI RuntimeService client.
// The CRI plugin in containerd serves on the same gRPC server.
func NewClient(socketPath string) (*Client, error) {
	socketPath = strings.TrimPrefix(socketPath, "unix://")
	addr := "unix://" + socketPath
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("dial CRI socket %s: %w", socketPath, err)
	}
	return &Client{
		runtime: runtimeapi.NewRuntimeServiceClient(conn),
		conn:    conn,
	}, nil
}

// Close closes the gRPC connection.
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// RunPodSandbox creates and starts a pod-level sandbox. Returns the sandbox ID.
func (c *Client) RunPodSandbox(ctx context.Context, config *runtimeapi.PodSandboxConfig, runtimeHandler string) (string, error) {
	resp, err := c.runtime.RunPodSandbox(ctx, &runtimeapi.RunPodSandboxRequest{
		Config:         config,
		RuntimeHandler: runtimeHandler,
	})
	if err != nil {
		return "", cgroups.MapRuntimeError(fmt.Errorf("RunPodSandbox: %w", err), cgroups.GetGlobalPolicy())
	}
	return resp.PodSandboxId, nil
}

// CreateContainer creates a new container in the given pod sandbox.
func (c *Client) CreateContainer(ctx context.Context, podSandboxID string, config *runtimeapi.ContainerConfig, sandboxConfig *runtimeapi.PodSandboxConfig) (string, error) {
	resp, err := c.runtime.CreateContainer(ctx, &runtimeapi.CreateContainerRequest{
		PodSandboxId:  podSandboxID,
		Config:        config,
		SandboxConfig: sandboxConfig,
	})
	if err != nil {
		return "", fmt.Errorf("CreateContainer: %w", err)
	}
	return resp.ContainerId, nil
}

// StartContainer starts the container.
func (c *Client) StartContainer(ctx context.Context, containerID string) error {
	_, err := c.runtime.StartContainer(ctx, &runtimeapi.StartContainerRequest{
		ContainerId: containerID,
	})
	return err
}

// StopContainer stops a running container with the given timeout (seconds).
func (c *Client) StopContainer(ctx context.Context, containerID string, timeout int64) error {
	_, err := c.runtime.StopContainer(ctx, &runtimeapi.StopContainerRequest{
		ContainerId: containerID,
		Timeout:     timeout,
	})
	return err
}

// RemoveContainer removes the container.
func (c *Client) RemoveContainer(ctx context.Context, containerID string) error {
	_, err := c.runtime.RemoveContainer(ctx, &runtimeapi.RemoveContainerRequest{
		ContainerId: containerID,
	})
	return err
}

// StopPodSandbox stops the pod sandbox and reclaims network resources.
func (c *Client) StopPodSandbox(ctx context.Context, podSandboxID string) error {
	_, err := c.runtime.StopPodSandbox(ctx, &runtimeapi.StopPodSandboxRequest{
		PodSandboxId: podSandboxID,
	})
	return err
}

// RemovePodSandbox removes the pod sandbox.
func (c *Client) RemovePodSandbox(ctx context.Context, podSandboxID string) error {
	_, err := c.runtime.RemovePodSandbox(ctx, &runtimeapi.RemovePodSandboxRequest{
		PodSandboxId: podSandboxID,
	})
	return err
}

// ContainerStatus returns the status of the container.
func (c *Client) ContainerStatus(ctx context.Context, containerID string) (*runtimeapi.ContainerStatusResponse, error) {
	return c.runtime.ContainerStatus(ctx, &runtimeapi.ContainerStatusRequest{
		ContainerId: containerID,
		Verbose:     false,
	})
}

// ContainerPID returns the host PID of a running CRI container.
func (c *Client) ContainerPID(ctx context.Context, containerID string) (int, error) {
	resp, err := c.runtime.ContainerStatus(ctx, &runtimeapi.ContainerStatusRequest{
		ContainerId: containerID,
		Verbose:     true,
	})
	if err != nil {
		return 0, err
	}
	if resp == nil {
		return 0, nil
	}
	return ParseContainerPID(resp.Info), nil
}

// ListContainers lists containers matching the filter.
func (c *Client) ListContainers(ctx context.Context, filter *runtimeapi.ContainerFilter) ([]*runtimeapi.Container, error) {
	resp, err := c.runtime.ListContainers(ctx, &runtimeapi.ListContainersRequest{
		Filter: filter,
	})
	if err != nil {
		return nil, err
	}
	return resp.Containers, nil
}

// PodSandboxStatus returns the status of the pod sandbox, including network IP.
func (c *Client) PodSandboxStatus(ctx context.Context, podSandboxID string) (*runtimeapi.PodSandboxStatusResponse, error) {
	return c.runtime.PodSandboxStatus(ctx, &runtimeapi.PodSandboxStatusRequest{
		PodSandboxId: podSandboxID,
		Verbose:      false,
	})
}

// ListPodSandboxes lists pod sandboxes matching the filter.
func (c *Client) ListPodSandboxes(ctx context.Context, filter *runtimeapi.PodSandboxFilter) ([]*runtimeapi.PodSandbox, error) {
	resp, err := c.runtime.ListPodSandbox(ctx, &runtimeapi.ListPodSandboxRequest{Filter: filter})
	if err != nil {
		return nil, err
	}
	return resp.Items, nil
}
