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

package controllers

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	ovsdpdkdrav1alpha1 "github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/api/ovsdpdkdra/v1alpha1"
	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/devicestate"
	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/deviceplugin"
	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/ovs"
)

const (
	resourcePolicySyncEventName = "resource-policy-sync"
)

// OvsDpdkResourcePolicyReconciler reconciles OvsDpdkResourcePolicy objects.
type OvsDpdkResourcePolicyReconciler struct {
	client.Client
	nodeName           string
	namespace          string
	log                klog.Logger
	deviceStateManager *devicestate.DeviceState
	ovsClient          ovs.Client
	dpManager          deviceplugin.ResourceUpdater
}

// NewOvsDpdkResourcePolicyReconciler creates a new OvsDpdkResourcePolicyReconciler.
func NewOvsDpdkResourcePolicyReconciler(
	c client.Client,
	nodeName, namespace string,
	deviceStateManager *devicestate.DeviceState,
	ovsClient ovs.Client,
	dpManager deviceplugin.ResourceUpdater,
) *OvsDpdkResourcePolicyReconciler {
	return &OvsDpdkResourcePolicyReconciler{
		Client:             c,
		nodeName:           nodeName,
		namespace:          namespace,
		log:                klog.Background().WithName("OvsDpdkResourcePolicyReconciler"),
		deviceStateManager: deviceStateManager,
		ovsClient:          ovsClient,
		dpManager:          dpManager,
	}
}

// Reconcile handles reconciliation of OvsDpdkResourcePolicy objects.
// It finds all policies in the watched namespace that match this node and
// forwards the consolidated bridge configuration to the device-state manager.
func (r *OvsDpdkResourcePolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.log.Info("Starting reconcile", "request", req.NamespacedName, "watchedNamespace", r.namespace)

	// Fetch only the node metadata to match NodeSelector terms against its labels and name.
	nodeMeta := &metav1.PartialObjectMetadata{}
	nodeMeta.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Node"))
	if err := r.Get(ctx, types.NamespacedName{Name: r.nodeName}, nodeMeta); err != nil {
		if apierrors.IsNotFound(err) {
			r.log.Error(err, "Node not found, requeuing", "nodeName", r.nodeName)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		r.log.Error(err, "Failed to get node", "nodeName", r.nodeName)
		return ctrl.Result{}, err
	}

	// List all OvsDpdkResourcePolicy objects in the watched namespace.
	policyList := &ovsdpdkdrav1alpha1.OvsDpdkResourcePolicyList{}
	if err := r.List(ctx, policyList, client.InNamespace(r.namespace)); err != nil {
		r.log.Error(err, "Failed to list OvsDpdkResourcePolicy objects", "namespace", r.namespace)
		return ctrl.Result{}, err
	}

	// Collect bridge specs from all matching policies, filtering out bridges
	// not yet present in OVS.
	var activeBridges []ovsdpdkdrav1alpha1.BridgeSpec
	for _, policy := range policyList.Items {
		if !r.matchesNodeSelector(nodeMeta, policy.Spec.NodeSelector) {
			r.log.V(2).Info("Policy does not match node, skipping",
				"policy", policy.Name, "nodeName", r.nodeName)
			continue
		}
		r.log.V(2).Info("Policy matches node, collecting bridges",
			"policy", policy.Name, "bridges", len(policy.Spec.Bridges))

		for _, b := range policy.Spec.Bridges {
			present, err := r.ovsClient.BridgeExists(b.Name)
			if err != nil {
				r.log.Error(err, "Failed to check bridge existence", "bridge", b.Name)
				return ctrl.Result{}, err
			}
			if !present {
				r.log.Info("Bridge not yet present in OVS, skipping", "bridge", b.Name)
				continue
			}
			activeBridges = append(activeBridges, b)
		}
	}

	r.log.Info("Reconciled policies", "matchingBridges", len(activeBridges))

	if err := r.deviceStateManager.UpdatePolicyDevices(ctx, activeBridges); err != nil {
		r.log.Error(err, "Failed to update policy devices")
		return ctrl.Result{}, err
	}

	if err := r.dpManager.UpdateResources(ctx, activeBridges); err != nil {
		r.log.Error(err, "Failed to update Device Plugin resources")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// matchesNodeSelector returns true if the given node satisfies the
// NodeSelector. A nil selector matches all nodes.
func (r *OvsDpdkResourcePolicyReconciler) matchesNodeSelector(
	nodeMeta *metav1.PartialObjectMetadata,
	ns *corev1.NodeSelector,
) bool {
	if ns == nil || len(ns.NodeSelectorTerms) == 0 {
		return true
	}

	selector, err := nodeaffinity.NewNodeSelector(ns)
	if err != nil {
		r.log.Error(err, "Failed to parse NodeSelector")
		return false
	}
	// Build a minimal Node from metadata; nodeaffinity.Match only reads
	// Labels and Name.
	node := &corev1.Node{ObjectMeta: nodeMeta.ObjectMeta}
	return selector.Match(node)
}

func (r *OvsDpdkResourcePolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	mapFn := func(ctx context.Context, obj client.Object) []reconcile.Request {
		return []reconcile.Request{
			{NamespacedName: types.NamespacedName{
				Namespace: r.namespace,
				Name:      resourcePolicySyncEventName,
			}},
		}
	}

	namespacePredicate := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return obj.GetNamespace() == r.namespace
	})

	nodePredicate := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return obj.GetName() == r.nodeName
	})

	nodeMetadata := &metav1.PartialObjectMetadata{}
	nodeMetadata.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Node"))

	triggerChan := make(chan event.GenericEvent, 1)

	r.ovsClient.SetBridgeNotifier(func(ev ovs.BridgeEvent) {
		select {
		case triggerChan <- event.GenericEvent{
			Object: &ovsdpdkdrav1alpha1.OvsDpdkResourcePolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourcePolicySyncEventName,
					Namespace: r.namespace,
				},
			},
		}:
		default:
		}
	})

	return ctrl.NewControllerManagedBy(mgr).
		Named("ovsdpdkresourcepolicy").
		Watches(&ovsdpdkdrav1alpha1.OvsDpdkResourcePolicy{},
			handler.EnqueueRequestsFromMapFunc(mapFn),
			builder.WithPredicates(namespacePredicate)).
		Watches(nodeMetadata,
			handler.EnqueueRequestsFromMapFunc(mapFn),
			builder.WithPredicates(nodePredicate)).
		WatchesRawSource(source.Channel(triggerChan, handler.EnqueueRequestsFromMapFunc(mapFn))).
		Complete(r)
}
