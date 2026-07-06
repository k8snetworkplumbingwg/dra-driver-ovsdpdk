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

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v2"

	"k8s.io/klog/v2"
	"k8s.io/klog/v2/textlogger"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/cdi"
	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/controllers"
	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/devicestate"
	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/driver"
	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/flags"
	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/ovs"
	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/socketfs"
	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/types"
)

const (
	defaultKubeletRegistrarDir = "/var/lib/kubelet/plugins_registry"
	defaultKubeletPluginsDir   = "/var/lib/kubelet/plugins"
)

func main() {
	if err := newApp().Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func newApp() *cli.App {
	f := &types.Flags{
		LoggingConfig: flags.NewLoggingConfig(),
	}

	cliFlags := []cli.Flag{
		&cli.StringFlag{
			Name:        "node-name",
			Usage:       "The name of the node on which the driver is running.",
			Required:    true,
			Destination: &f.NodeName,
			EnvVars:     []string{"NODE_NAME"},
		},
		&cli.StringFlag{
			Name:        "namespace",
			Usage:       "Namespace where the driver watches for OvsDpdkResourcePolicy resources.",
			Value:       consts.DefaultNamespace,
			Destination: &f.Namespace,
			EnvVars:     []string{"NAMESPACE"},
		},
		&cli.StringFlag{
			Name:        "config-name",
			Usage:       "Name of the OvsDpdkConfig cluster-scoped object to watch.",
			Value:       "default",
			Destination: &f.ConfigName,
			EnvVars:     []string{"CONFIG_NAME"},
		},
		&cli.StringFlag{
			Name:        "cdi-root",
			Usage:       "Absolute path to the directory where CDI files will be generated.",
			Value:       "/var/run/cdi",
			Destination: &f.CdiRoot,
			EnvVars:     []string{"CDI_ROOT"},
		},
		&cli.StringFlag{
			Name:        "kubelet-registrar-directory-path",
			Usage:       "Absolute path to the directory where kubelet stores plugin registrations.",
			Value:       defaultKubeletRegistrarDir,
			Destination: &f.KubeletRegistrarDirectoryPath,
			EnvVars:     []string{"KUBELET_REGISTRAR_DIRECTORY_PATH"},
		},
		&cli.StringFlag{
			Name:        "kubelet-plugins-directory-path",
			Usage:       "Absolute path to the directory where kubelet stores plugin data.",
			Value:       defaultKubeletPluginsDir,
			Destination: &f.KubeletPluginsDirectoryPath,
			EnvVars:     []string{"KUBELET_PLUGINS_DIRECTORY_PATH"},
		},
		&cli.StringFlag{
			Name:        "ovs-rundir",
			Usage:       "Absolute path to the OVS run directory containing db.sock.",
			Value:       ovs.DefaultOVSRunDir,
			Destination: &f.OVSRunDir,
			EnvVars:     []string{"OVS_RUNDIR"},
		},
		&cli.BoolFlag{
			Name:        "enable-device-metadata",
			Usage:       "Enable DRA DownwardAPI device metadata (KEP-5304). When enabled, a JSON metadata file is bind-mounted into each container with device attributes such as the vhost-user socket path.",
			Value:       false,
			Destination: &f.EnableDeviceMetadata,
			EnvVars:     []string{"ENABLE_DEVICE_METADATA"},
		},
	}
	cliFlags = append(cliFlags, f.KubeClientConfig.Flags()...)
	cliFlags = append(cliFlags, f.LoggingConfig.Flags()...)

	app := &cli.App{
		Name:            "dra-driver-ovsdpdk",
		Usage:           "dra-driver-ovsdpdk implements a DRA driver for OVS-DPDK vhost-user ports.",
		ArgsUsage:       " ",
		HideHelpCommand: true,
		Flags:           cliFlags,
		Before: func(c *cli.Context) error {
			if c.Args().Len() > 0 {
				return fmt.Errorf("arguments not supported: %v", c.Args().Slice())
			}
			if err := f.LoggingConfig.Apply(); err != nil {
				return err
			}
			// Wire controller-runtime to use the same klog backend so its
			// internal logs are not silently dropped.
			ctrl.SetLogger(textlogger.NewLogger(textlogger.NewConfig()))
			return nil
		},
		Action: func(c *cli.Context) error {
			restCfg, err := f.KubeClientConfig.RestConfig()
			if err != nil {
				return fmt.Errorf("create REST config: %v", err)
			}

			k8sClient, err := f.KubeClientConfig.NewCoreClient()
			if err != nil {
				return fmt.Errorf("create client: %v", err)
			}

			mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
				Scheme: flags.Scheme,
				Metrics: metricsserver.Options{
					BindAddress: "0", // disabled
				},
				HealthProbeBindAddress: "0", // disabled
				LeaderElection:         false,
				// Restrict the cache for namespaced resources to the driver's
				// own namespace so that only a namespaced Role (not a
				// ClusterRole) is required to list/watch them.
				Cache: cache.Options{
					DefaultNamespaces: map[string]cache.Config{
						f.Namespace: {},
					},
				},
			})
			if err != nil {
				return fmt.Errorf("create controller manager: %v", err)
			}

			config := &types.Config{
				Flags:     f,
				K8sClient: k8sClient,
				Manager:   mgr,
			}

			return run(c.Context, config)
		},
	}

	return app
}

// run is the main entry point after flag parsing. It wires up all components
// and blocks until a signal is received or a fatal error occurs.
func run(ctx context.Context, config *types.Config) error {
	logger := klog.FromContext(ctx).WithName("main")

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	defer stop()
	ctx, cancel := context.WithCancelCause(ctx)
	config.CancelMainCtx = cancel

	if err := os.MkdirAll(config.DriverPluginPath(), 0750); err != nil {
		return fmt.Errorf("create driver plugin path %q: %w", config.DriverPluginPath(), err)
	}

	if err := os.MkdirAll(config.Flags.CdiRoot, 0750); err != nil {
		return fmt.Errorf("create CDI root %q: %w", config.Flags.CdiRoot, err)
	}

	logger.Info("Starting dra-driver-ovsdpdk",
		"node", config.Flags.NodeName,
		"namespace", config.Flags.Namespace,
		"driverName", consts.DriverName,
	)

	ovsClient, err := ovs.New(ctx, config.Flags.OVSRunDir)
	if err != nil {
		return fmt.Errorf("create OVS client: %w", err)
	}
	defer ovsClient.Close()

	cdiHandler, err := cdi.New(config.Flags.CdiRoot)
	if err != nil {
		return fmt.Errorf("create DRI Handler: %w", err)
	}

	devState := devicestate.New(cdiHandler, socketfs.New(), ovsClient)

	driverConfig := driver.Config{
		NodeName:             config.Flags.NodeName,
		EnableDeviceMetadata: config.Flags.EnableDeviceMetadata,
		PluginDataDir:        config.DriverPluginPath(),
		CdiDir:               config.Flags.CdiRoot,
	}
	dvr, err := driver.New(ctx, devState, config.K8sClient, &driverConfig)
	if err != nil {
		return fmt.Errorf("create DRA driver: %w", err)
	}
	defer dvr.Stop()

	reconciler := controllers.NewOvsDpdkResourcePolicyReconciler(
		config.Manager.GetClient(),
		config.Flags.NodeName,
		config.Flags.Namespace,
		devState,
		ovsClient,
	)
	if err := reconciler.SetupWithManager(config.Manager); err != nil {
		return fmt.Errorf("setup controller: %w", err)
	}

	configReconciler := controllers.NewOvsDpdkConfigReconciler(
		config.Manager.GetClient(),
		config.Flags.ConfigName,
		devState,
	)
	if err := configReconciler.SetupWithManager(config.Manager); err != nil {
		return fmt.Errorf("setup config controller: %w", err)
	}

	mgrErrCh := make(chan error, 1)
	go func() {
		if err := config.Manager.Start(ctx); err != nil {
			mgrErrCh <- fmt.Errorf("controller manager exited: %w", err)
		}
		close(mgrErrCh)
	}()

	select {
	case <-ctx.Done():
	case err := <-mgrErrCh:
		if err != nil {
			config.CancelMainCtx(err)
		}
	}

	stop() // restore default signal handling as soon as possible
	if err := context.Cause(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error(err, "Shutting down due to error")
		return err
	}
	logger.V(1).Info("Shutting down cleanly")
	return nil
}
