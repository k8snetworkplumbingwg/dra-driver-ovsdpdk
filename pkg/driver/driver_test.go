package driver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	healthv1 "k8s.io/kubelet/pkg/apis/dra-health/v1"
	healthalpha "k8s.io/kubelet/pkg/apis/dra-health/v1alpha1"
	drapb "k8s.io/kubelet/pkg/apis/dra/v1"
)

var _ = ginkgo.Describe("Kubernetes v0.37 plugin compatibility", func() {
	ginkgo.DescribeTable("serves the supported plugin APIs",
		func(metadata bool) {
			// Short socket paths also work when the checkout path is long.
			pluginDir, err := os.MkdirTemp("", "dra-")
			Expect(err).NotTo(HaveOccurred())
			ginkgo.DeferCleanup(func() {
				Expect(os.RemoveAll(pluginDir)).To(Succeed())
			})
			cdiDir, err := os.MkdirTemp("", "cdi-")
			Expect(err).NotTo(HaveOccurred())
			ginkgo.DeferCleanup(func() {
				Expect(os.RemoveAll(cdiDir)).To(Succeed())
			})

			config := &Config{
				NodeName: "node", EnableDeviceMetadata: metadata,
				PluginDataDir: pluginDir, CdiDir: cdiDir,
			}
			opts := append(pluginOptions(config, fake.NewClientset()),
				kubeletplugin.RegistrationService(false),
				kubeletplugin.NodeUID("node-uid"),
			)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			ginkgo.DeferCleanup(cancel)
			helper, err := kubeletplugin.Start(ctx, &Driver{}, opts...)
			Expect(err).NotTo(HaveOccurred(), "start plugin")
			ginkgo.DeferCleanup(helper.Stop)

			conn, err := grpc.NewClient("unix://"+filepath.Join(pluginDir, "dra.sock"),
				grpc.WithTransportCredentials(insecure.NewCredentials()))
			Expect(err).NotTo(HaveOccurred())
			ginkgo.DeferCleanup(func() {
				Expect(conn.Close()).To(Succeed())
			})

			// Kubernetes 1.36 and 1.37 both use the stable DRA v1 service.
			client := drapb.NewDRAPluginClient(conn)
			_, err = client.NodePrepareResources(ctx, &drapb.NodePrepareResourcesRequest{})
			Expect(err).NotTo(HaveOccurred(), "prepare through DRA v1")
			_, err = client.NodeUnprepareResources(ctx, &drapb.NodeUnprepareResourcesRequest{})
			Expect(err).NotTo(HaveOccurred(), "unprepare through DRA v1")

			// 1.36 uses alpha health; 1.37 can negotiate stable health.
			alpha, err := healthalpha.NewDRAResourceHealthClient(conn).NodeWatchResources(ctx, &healthalpha.NodeWatchResourcesRequest{})
			if err == nil {
				_, err = alpha.Recv()
			}
			Expect(status.Code(err)).To(Equal(codes.Unimplemented), "alpha health: %v", err)
			stable, err := healthv1.NewDRAResourceHealthClient(conn).NodeWatchResources(ctx, &healthv1.NodeWatchResourcesRequest{})
			if err == nil {
				_, err = stable.Recv()
			}
			Expect(status.Code(err)).To(Equal(codes.Unimplemented), "stable health: %v", err)
		},
		ginkgo.Entry("without metadata", false),
		ginkgo.Entry("with metadata", true),
	)

	ginkgo.It("reports health monitoring as unsupported without sending or closing the channel", func() {
		reports := make(chan kubeletplugin.DeviceHealthReport)
		err := (&Driver{}).WatchHealthStatus(context.Background(), reports)
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, kubeletplugin.ErrHealthNotSupported)).To(BeTrue())
		select {
		case <-reports:
			ginkgo.Fail("unsupported health must not send reports or close the channel")
		default:
		}
	})
})
