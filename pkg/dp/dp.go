/*
 * Copyright 2026 Red Hat, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package dp

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

// TopologyDPServer represents a Topology Device Plugin.
type TopologyDPServer interface {
	start(ctx context.Context) error
	stop()
	GetNUMA() int
}

// Server is a Device Plugin gRPC server that exposes a fixed set of fake
// devices carrying NUMA topology information for a single OVS bridge.
// The NUMA node is immutable after start; to change it, stop and recreate.
type Server struct {
	pluginapi.UnimplementedDevicePluginServer

	resourceName string
	numaNode     int
	deviceCount  int

	socketPath string
	grpcServer *grpc.Server
	log        klog.Logger
}

// newServer creates a Server for the given resource name, NUMA node, and
// device count. The resource name must already be fully qualified.
func newServer(resourceName string, numaNode, deviceCount int) *Server {
	return &Server{
		resourceName: resourceName,
		numaNode:     numaNode,
		deviceCount:  deviceCount,
		socketPath:   socketPath(resourceName),
		log:          klog.Background().WithName("dp.Server").WithValues("resource", resourceName),
	}
}

// socketPath returns the unix socket path for the given resource name.
// Slashes and dots are replaced with dashes to form a valid filename.
func socketPath(resourceName string) string {
	sanitized := strings.NewReplacer("/", "-", ".", "-").Replace(resourceName)
	return filepath.Join(pluginapi.DevicePluginPath, sanitized+".sock")
}

// start starts the gRPC server and registers with the kubelet.
func (s *Server) start(ctx context.Context) error {
	if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale socket %q: %w", s.socketPath, err)
	}

	lis, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen on %q: %w", s.socketPath, err)
	}

	s.grpcServer = grpc.NewServer()
	pluginapi.RegisterDevicePluginServer(s.grpcServer, s)

	go func() {
		if err := s.grpcServer.Serve(lis); err != nil {
			s.log.Error(err, "gRPC server exited")
		}
	}()

	if err := s.register(ctx); err != nil {
		s.grpcServer.Stop()
		_ = os.Remove(s.socketPath)
		return fmt.Errorf("register with kubelet: %w", err)
	}

	s.log.Info("Device Plugin started", "socket", s.socketPath, "numaNode", s.numaNode)
	return nil
}

// register dials the kubelet registration socket and calls Register.
func (s *Server) register(ctx context.Context) error {
	conn, err := grpc.NewClient(
		"unix://"+pluginapi.KubeletSocket,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return fmt.Errorf("dial kubelet socket: %w", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			s.log.V(2).Info("Failed to close kubelet registration connection", "err", err)
		}
	}()

	client := pluginapi.NewRegistrationClient(conn)
	_, err = client.Register(ctx, &pluginapi.RegisterRequest{
		Version:      pluginapi.Version,
		Endpoint:     filepath.Base(s.socketPath),
		ResourceName: s.resourceName,
	})
	return err
}

// stop gracefully stops the gRPC server and removes the socket.
func (s *Server) stop() {
	if s.grpcServer != nil {
		s.grpcServer.GracefulStop()
	}
	if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
		s.log.Error(err, "Failed to remove socket", "path", s.socketPath)
	}
	s.log.Info("Device Plugin stopped")
}

// devices builds the device list. Each device gets its own TopologyInfo.
func (s *Server) devices() []*pluginapi.Device {
	devs := make([]*pluginapi.Device, s.deviceCount)
	for i := range devs {
		devs[i] = &pluginapi.Device{
			ID:     fmt.Sprintf("device-%d", i),
			Health: pluginapi.Healthy,
			Topology: &pluginapi.TopologyInfo{
				Nodes: []*pluginapi.NUMANode{{ID: int64(s.numaNode)}},
			},
		}
	}
	return devs
}

// GetDevicePluginOptions implements DevicePluginServer.
// GetNUMA returns the NUMA node this server is advertising.
func (s *Server) GetNUMA() int {
	return s.numaNode
}

func (s *Server) GetDevicePluginOptions(_ context.Context, _ *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	return &pluginapi.DevicePluginOptions{}, nil
}

// ListAndWatch implements DevicePluginServer. It sends the device list once
// and then blocks until the stream context is done. The device list is static
// for the lifetime of the server; NUMA changes are handled by stop+recreate.
func (s *Server) ListAndWatch(_ *pluginapi.Empty, stream pluginapi.DevicePlugin_ListAndWatchServer) error {
	if err := stream.Send(&pluginapi.ListAndWatchResponse{Devices: s.devices()}); err != nil {
		return err
	}
	<-stream.Context().Done()
	return nil
}

// Allocate implements DevicePluginServer. Returns empty responses — this
// Device Plugin exists only to carry topology information.
func (s *Server) Allocate(_ context.Context, req *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	resp := &pluginapi.AllocateResponse{
		ContainerResponses: make([]*pluginapi.ContainerAllocateResponse, len(req.ContainerRequests)),
	}
	for i := range resp.ContainerResponses {
		resp.ContainerResponses[i] = &pluginapi.ContainerAllocateResponse{}
	}
	return resp, nil
}

// GetPreferredAllocation implements DevicePluginServer.
func (s *Server) GetPreferredAllocation(_ context.Context, _ *pluginapi.PreferredAllocationRequest) (*pluginapi.PreferredAllocationResponse, error) {
	return &pluginapi.PreferredAllocationResponse{}, nil
}

// PreStartContainer implements DevicePluginServer.
func (s *Server) PreStartContainer(_ context.Context, _ *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	return &pluginapi.PreStartContainerResponse{}, nil
}
