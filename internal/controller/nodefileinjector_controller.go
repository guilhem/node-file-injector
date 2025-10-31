/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"

	"github.com/guilhem/node-file-injector/internal/controller/deployer"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	filesv1alpha1 "github.com/guilhem/node-file-injector/api/v1alpha1"
)

const (
	nodeFileInjectorFinalizer = "files.barpilot.io/finalizer"

	// Condition types
	ConditionReady = "Ready"

	// Condition reasons
	ReasonSucceeded   = "Succeeded"
	ReasonFailed      = "Failed"
	ReasonInvalidSpec = "InvalidSpec"
)

// NodeFileInjectorReconciler reconciles a NodeFileInjector object
type NodeFileInjectorReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=files.barpilot.io,resources=nodefileinjectors,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=files.barpilot.io,resources=nodefileinjectors/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=files.barpilot.io,resources=nodefileinjectors/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *NodeFileInjectorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Fetch the NodeFileInjector resource
	var nfi filesv1alpha1.NodeFileInjector
	if err := r.Get(ctx, req.NamespacedName, &nfi); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Always update status at the end
	defer func() {
		r.updateResourceStatus(ctx, &nfi)
	}()

	// Add finalizer if not present (without requeuing)
	if !controllerutil.ContainsFinalizer(&nfi, nodeFileInjectorFinalizer) {
		op, err := controllerutil.CreateOrPatch(ctx, r.Client, &nfi, func() error {
			controllerutil.AddFinalizer(&nfi, nodeFileInjectorFinalizer)
			return nil
		})
		if err != nil {
			r.Recorder.Event(&nfi, corev1.EventTypeWarning, "FinalizerFailed",
				fmt.Sprintf("Failed to add finalizer: %v", err))
			return ctrl.Result{}, fmt.Errorf("failed to add finalizer: %w", err)
		}
		if op != controllerutil.OperationResultNone {
			r.Recorder.Event(&nfi, corev1.EventTypeNormal, "FinalizerAdded",
				"Finalizer added successfully")
		}
	}

	// Handle deletion vs normal reconciliation
	if !nfi.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &nfi)
	}

	return r.reconcileNormal(ctx, &nfi)
}

// nolint:unparam
func (r *NodeFileInjectorReconciler) reconcileNormal(ctx context.Context, nfi *filesv1alpha1.NodeFileInjector) (ctrl.Result, error) {
	// Validate spec
	if err := nfi.Spec.ValidateSource(); err != nil {
		r.setCondition(nfi, ConditionReady, metav1.ConditionFalse, ReasonInvalidSpec, err.Error())
		r.Recorder.Event(nfi, corev1.EventTypeWarning, ReasonInvalidSpec,
			fmt.Sprintf("Invalid spec: %v", err))
		return ctrl.Result{}, nil
	}

	// Determine deployment mode (default to DaemonSet for backward compatibility)
	mode := nfi.Spec.Mode
	if mode == "" {
		mode = filesv1alpha1.DeploymentModeDaemonSet
	}

	// Create deployer for the selected mode
	d := deployer.NewDeployer(mode, r.Client, r.Scheme, r.Recorder)

	// Reconcile using the deployer
	status, err := d.Reconcile(ctx, nfi)
	if err != nil {
		r.setCondition(nfi, ConditionReady, metav1.ConditionFalse, ReasonFailed, err.Error())
		return ctrl.Result{}, err
	}

	// Merge status from deployer
	nfi.Status.NodesMatched = status.NodesMatched
	nfi.Status.DaemonSetName = status.DaemonSetName
	nfi.Status.NodeStatus = status.NodeStatuses

	// Merge conditions from deployer
	for _, condition := range status.Conditions {
		meta.SetStatusCondition(&nfi.Status.Conditions, condition)
	}

	return ctrl.Result{}, nil
}

// nolint:unparam
func (r *NodeFileInjectorReconciler) reconcileDelete(ctx context.Context, nfi *filesv1alpha1.NodeFileInjector) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Check if our finalizer is present
	if !controllerutil.ContainsFinalizer(nfi, nodeFileInjectorFinalizer) {
		return ctrl.Result{}, nil
	}

	log.Info("Performing cleanup for resource deletion", "name", nfi.Name)

	// Determine deployment mode
	mode := nfi.Spec.Mode
	if mode == "" {
		mode = filesv1alpha1.DeploymentModeDaemonSet
	}

	// Create deployer for cleanup
	d := deployer.NewDeployer(mode, r.Client, r.Scheme, r.Recorder)

	// Delete resources using the deployer
	if err := d.Delete(ctx, nfi); err != nil {
		log.Error(err, "Failed to delete resources")
		return ctrl.Result{}, err
	}

	// Remove finalizer
	op, err := controllerutil.CreateOrPatch(ctx, r.Client, nfi, func() error {
		controllerutil.RemoveFinalizer(nfi, nodeFileInjectorFinalizer)
		return nil
	})
	if err != nil {
		log.Error(err, "Failed to remove finalizer")
		r.Recorder.Event(nfi, corev1.EventTypeWarning, "FinalizerRemovalFailed",
			fmt.Sprintf("Failed to remove finalizer: %v", err))
		return ctrl.Result{}, err
	}
	if op != controllerutil.OperationResultNone {
		r.Recorder.Event(nfi, corev1.EventTypeNormal, "FinalizerRemoved",
			"Finalizer removed successfully")
	}

	log.Info("Successfully completed resource deletion", "name", nfi.Name)
	return ctrl.Result{}, nil
}

// nolint:unparam
func (r *NodeFileInjectorReconciler) setCondition(nfi *filesv1alpha1.NodeFileInjector, conditionType string, status metav1.ConditionStatus, reason, message string) {
	condition := metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: nfi.Generation,
	}

	meta.SetStatusCondition(&nfi.Status.Conditions, condition)
}

func (r *NodeFileInjectorReconciler) updateResourceStatus(ctx context.Context, nfi *filesv1alpha1.NodeFileInjector) {
	log := logf.FromContext(ctx)

	// Fetch the latest version to ensure correct resource version
	var latestNFI filesv1alpha1.NodeFileInjector
	if err := r.Get(ctx, types.NamespacedName{
		Name:      nfi.Name,
		Namespace: nfi.Namespace,
	}, &latestNFI); err != nil {
		// Ignore NotFound errors - resource was deleted
		if !apierrors.IsNotFound(err) {
			log.Error(err, "Failed to get latest resource for status update")
		}
		return
	}

	// Copy status fields from working copy to latest version
	latestNFI.Status = nfi.Status

	if err := r.Status().Update(ctx, &latestNFI); err != nil {
		// Ignore NotFound and Conflict errors - expected during concurrent updates/deletion
		if !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			log.Error(err, "Failed to update status")
		}
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeFileInjectorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&filesv1alpha1.NodeFileInjector{}).
		Owns(&appsv1.DaemonSet{}).
		Owns(&batchv1.Job{}).
		Named("nodefileinjector").
		Complete(r)
}
