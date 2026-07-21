# dra-driver-ovsdpdk

A Kubernetes [Dynamic Resource Allocation (DRA)](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/) driver that exposes OVS-DPDK bridges as schedulable devices. Each allocated device gets a unique per-pod vhost-user socket directory, bind-mounted into the container via CDI.

> **Status:** proof-of-concept / experimentation.

## How it works

1. An `OvsDpdkResourcePolicy` CRD (namespaced) tells the driver which OVS bridges to advertise on which nodes.
2. An `OvsDpdkConfig` CRD (cluster-scoped) defines global vhost-user socket settings — ownership, ACLs, SELinux labels, and the container mount path.
3. The driver runs as a DaemonSet. On each node it reconciles matching policies and publishes a `ResourceSlice` listing the bridges as DRA devices (with `AllowMultipleAllocations=true`) (see [DRA consumable-capacity](https://kubernetes.io/blog/2025/09/18/kubernetes-v1-34-dra-consumable-capacity/)).
4. When a pod claims a device, the driver creates a per-pod socket directory on the host, writes a CDI spec that bind-mounts it into the container, and updates `ResourceClaim.Status.Devices` with the mount and socket paths.
5. On pod deletion the directory and CDI spec are removed.

## Configuration

### OvsDpdkConfig (cluster-scoped)

Defines global vhost-user socket directory settings. The driver watches the
object whose name matches the `--config-name` flag (default: `default`).

```yaml
apiVersion: ovsdpdk.k8snetworkplumbingwg.io/v1alpha1
kind: OvsDpdkConfig
metadata:
  name: default
spec:
  vhostUser:
    containerRootPath: /var/run/ovsdpdk
    user: openvswitch       # name or numeric UID
    group: 107              # name or numeric GID
    selinuxLabel: "system_u:object_r:container_file_t:s0"
    aclUsers:
      - openvswitch
```

| Field | Required | Description |
|---|---|---|
| `vhostUser.containerRootPath` | no | Container root for CDI mount. Default: `/var/run/ovsdpdk` |
| `vhostUser.user` | no | Owner of the socket directory (name or UID) |
| `vhostUser.group` | no | Group of the socket directory (name or GID) |
| `vhostUser.selinuxLabel` | no | SELinux label applied to the socket directory (`user:role:type:level`) |
| `vhostUser.aclUsers` | no | Users granted access via `setfacl` |

> The host root path is fixed at `/var/run/ovsdpdk`.

### OvsDpdkResourcePolicy (namespaced)

Defines which OVS bridges to advertise as DRA devices and on which nodes.

```yaml
apiVersion: ovsdpdk.k8snetworkplumbingwg.io/v1alpha1
kind: OvsDpdkResourcePolicy
metadata:
  name: worker-policy
  namespace: dra-driver-ovsdpdk
spec:
  # nodeSelector restricts which nodes this policy applies to.
  # Omit to apply to all nodes.
  nodeSelector:
    nodeSelectorTerms:
      - matchExpressions:
          - key: node-role.kubernetes.io/worker
            operator: Exists
  bridges:
    - name: br-dpdk0
    - name: br-dpdk1
```

| Field | Required | Description |
|---|---|---|
| `bridges[].name` | yes | OVS bridge name to advertise as a DRA device |
| `nodeSelector` | no | Limit to matching nodes; omit for all nodes |


### OvsPortConfig (per-allocation, in ResourceClaim)

Allows per-allocation customization of OVS port properties. Embed it in the `devices.config` stanza of a `ResourceClaim` or `ResourceClaimTemplate`; the driver reads and validates it when preparing the device.

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  name: dpdk-port-tagged
spec:
  spec:
    devices:
      requests:
        - name: vhost-port
          exactly:
            deviceClassName: ovsdpdk
      config:
        - opaque:
            driver: ovsdpdk.k8snetworkplumbingwg.io
            parameters:
              apiVersion: ovsdpdk.k8snetworkplumbingwg.io/v1alpha1
              kind: OvsPortConfig
              vlan: 100
              policing:
                max_rate: 100000
                burst: 10000
```

Field specification:

| Field | Required | Description |
|---|---|---|
| `vlan` | no | VLAN ID to tag the OVS port (0–4095). Omit for an untagged port. |
| `policing.max_rate` | yes (if `policing` is set) | Maximum ingress rate in kbps (`ingress_policing_rate`). 0 means unlimited. |
| `policing.burst` | no | Maximum ingress burst size in kb (`ingress_policing_burst`). 0 or omitted means OVS default. |


> The `kind` must be `OvsPortConfig` and `apiVersion` must be `ovsdpdk.k8snetworkplumbingwg.io/v1alpha1`; the driver rejects configs with mismatched values.

## Deploying

### Prerequisites

- Kubernetes ≥ 1.36 with these feature gates enabled on the API server, scheduler, and all kubelets:
  ```
  --feature-gates=DRAConsumableCapacity=true,DRAResourceClaimDeviceStatus=true
  ```
  For kubeadm, set `featureGates` in `KubeletConfiguration`, `KubeSchedulerConfiguration`, and the API server `ClusterConfiguration`. For OpenShift, use the `FeatureGate` CR.
- OVS-DPDK installed on worker nodes with the bridges already created.
- A container registry reachable from the cluster nodes.

### Build and push

```bash
export IMAGE_NAME=ghcr.io/k8snetworkplumbingwg/dra-driver-ovsdpdk  # adjust as needed
export IMAGE_TAG=latest
make build-image
podman push "${IMAGE_NAME}:${IMAGE_TAG}"
```

### Deploy on Kubernetes

```bash
# CRD, namespace, RBAC, and DaemonSet in one shot:
make deploy

# Or step by step:
kubectl apply -f deployments/crds/
kubectl kustomize deployments/k8s/ | \
    sed "s|IMAGE|${IMAGE_NAME}:${IMAGE_TAG}|g" | \
    kubectl apply -f -
```

Wait for rollout:

```bash
kubectl rollout status daemonset/dra-driver-ovsdpdk -n dra-driver-ovsdpdk
kubectl logs -n dra-driver-ovsdpdk -l app=dra-driver-ovsdpdk --prefix
```

### Configure the driver

Apply the global config and a resource policy.

```bash
# Global vhost-user settings (edit to match your environment):
kubectl apply -f deployments/examples/k8s-config.yaml

# Bridge policy (edit bridges and nodeSelector):
kubectl apply -f deployments/examples/policy.yaml
```

Verify the driver published ResourceSlices:

```bash
kubectl get resourceslices -o wide
```

### Consume a device

The recommended pattern is a `ResourceClaimTemplate` so that each pod gets its
own claim. The socket path is built from the pod-local claim name (here `vhost`)
and the request name (here `vhost-port`), giving stable, predictable paths
across pod restarts and VM migrations. A single claim can contain multiple
requests, each getting its own socket directory:

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  name: dpdk-port
spec:
  spec:
    devices:
      requests:
        - name: vhost-port
          exactly:
            deviceClassName: ovsdpdk
            selectors:
              - cel:
                  expression: 'device.attributes["ovsdpdk.k8snetworkplumbingwg.io"].bridgeName == "br-dpdk0"'
---
apiVersion: v1
kind: Pod
metadata:
  name: my-dpdk-pod
spec:
  restartPolicy: Never
  resourceClaims:
    - name: vhost
      resourceClaimTemplateName: dpdk-port
  containers:
    - name: app
      image: quay.io/fedora/fedora-minimal:latest
      command: ["/bin/bash"]
      args:
        - "-c"
        - "sleep INF"
      resources:
        claims:
          - name: vhost
```

The CEL selector pins scheduling to the node that owns `br-dpdk0`. After the
pod starts, inspect the claim status for the exact socket paths:

```bash
kubectl get resourceclaim
```
```
NAME                      STATE                AGE
my-dpdk-pod-vhost-p5bzb   allocated,reserved   7m58s
```
```bash
kubectl get resourceclaim my-dpdk-pod-vhost-p6bzb \
  -o jsonpath='{.status.devices[0].data}' | jq .
```

```json
{
  "bridgeName": "br-dpdk0",
  "cdiDeviceID": "ovsdpdk.k8snetworkplumbingwg.io/vhost-user=aaa85ca7",
  "mount": {
    "containerDir": "/var/run/ovsdpdk/vhost/vhost-port",
    "hostDir": "/var/run/ovsdpdk/c362b1d7-d4ea-4efe-9e90-e4cd83131baf_vhost_vhost-port"
  },
  "socket": {
    "containerPath": "/var/run/ovsdpdk/vhost/vhost-port/vhost.sock",
    "hostPath": "/var/run/ovsdpdk/c362b1d7-d4ea-4efe-9e90-e4cd83131baf_vhost_vhost-port/vhost.sock"
  }
}
```

> **Hand-written claims**: if you create a `ResourceClaim` directly (without a
> template), the driver uses the claim's name for the socket path.

### Uninstall

```bash
make undeploy
# also remove config and policies:
kubectl delete -f deployments/examples/k8s-config.yaml
kubectl delete -f deployments/examples/policy.yaml
```

### Deploying on OpenShift

```bash
make deploy-openshift
```

Then apply the OpenShift-specific config and a bridge policy:

```bash
kubectl apply -f deployments/examples/openshift-config.yaml
kubectl apply -f deployments/examples/policy.yaml
```

To remove:

```bash
make undeploy-openshift
kubectl delete -f deployments/examples/openshift-config.yaml
kubectl delete -f deployments/examples/policy.yaml
```

## Development

```bash
make build    # compile
make test     # unit tests
make check    # vet + lint
make generate # regenerate CRD manifests and deepcopy
```

## Device metadata (KEP-5304 DownwardAPI)

When the driver is started with `--enable-device-metadata` (or `ENABLE_DEVICE_METADATA=true`), it uses the built-in support in `k8s.io/dynamic-resource-allocation` to write a versioned metadata JSON file for each prepared device and bind-mount it read-only into the container at the standard KEP-5304 path:

```
/var/run/kubernetes.io/dra-device-attributes/<pod-claim-name>/<request-name>/metadata.json
```

The file is a JSON stream in the `metadata.resource.k8s.io/v1alpha1` format. It contains the following attributes:

| Attribute key | Value |
|---|---|
| `vhost-user-path` | Container-side path of the vhost-user socket |
| `mtu` | Custom MTU value (Optional) |

