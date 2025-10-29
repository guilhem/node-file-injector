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
	_ "embed"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
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

//go:embed scripts/file-injector.sh
var fileInjectorScript string

const (
	nodeFileInjectorFinalizer = "files.barpilot.io/finalizer"

	// Condition types
	ConditionReady = "Ready"

	// Condition reasons
	ReasonSucceeded        = "Succeeded"
	ReasonFailed           = "Failed"
	ReasonInvalidSpec      = "InvalidSpec"
	ReasonDaemonSetCreated = "DaemonSetCreated"
	ReasonDaemonSetUpdated = "DaemonSetUpdated"
	ReasonDaemonSetFailed  = "DaemonSetFailed"
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

	// Reconcile the DaemonSet
	daemonSet, err := r.reconcileDaemonSet(ctx, nfi)
	if err != nil {
		r.setCondition(nfi, ConditionReady, metav1.ConditionFalse, ReasonFailed, err.Error())
		return ctrl.Result{}, err
	}

	// Update status
	nfi.Status.DaemonSetName = daemonSet.Name

	// Count matching nodes
	var nodeList corev1.NodeList
	if err := r.List(ctx, &nodeList, client.MatchingLabels(nfi.Spec.NodeSelector)); err != nil {
		// Ignore error, just don't update count
	} else {
		nfi.Status.NodesMatched = int32(len(nodeList.Items))
	}

	// Success
	r.setCondition(nfi, ConditionReady, metav1.ConditionTrue, ReasonSucceeded, "DaemonSet is ready")

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

	// Delete the DaemonSet if it exists
	if nfi.Status.DaemonSetName != "" {
		daemonSet := &appsv1.DaemonSet{}
		err := r.Get(ctx, types.NamespacedName{
			Name:      nfi.Status.DaemonSetName,
			Namespace: nfi.Namespace,
		}, daemonSet)

		if err == nil {
			if err := r.Delete(ctx, daemonSet); err != nil && !apierrors.IsNotFound(err) {
				log.Error(err, "Failed to delete DaemonSet")
				return ctrl.Result{}, err
			}
			log.Info("DaemonSet deleted", "name", daemonSet.Name)
		}
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

func (r *NodeFileInjectorReconciler) reconcileDaemonSet(ctx context.Context, nfi *filesv1alpha1.NodeFileInjector) (*appsv1.DaemonSet, error) {
	log := logf.FromContext(ctx)

	// Build desired DaemonSet
	desired := r.buildDaemonSet(nfi)

	// Get current DaemonSet
	current := &appsv1.DaemonSet{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      desired.Name,
		Namespace: desired.Namespace,
	}, current)

	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, err
		}

		// Create DaemonSet
		if err := controllerutil.SetControllerReference(nfi, desired, r.Scheme); err != nil {
			return nil, err
		}

		if err := r.Create(ctx, desired); err != nil {
			r.Recorder.Event(nfi, corev1.EventTypeWarning, ReasonDaemonSetFailed,
				fmt.Sprintf("Failed to create DaemonSet: %v", err))
			return nil, err
		}

		r.Recorder.Event(nfi, corev1.EventTypeNormal, ReasonDaemonSetCreated,
			fmt.Sprintf("DaemonSet %s created", desired.Name))
		log.Info("DaemonSet created", "name", desired.Name)
		return desired, nil
	}

	// Update DaemonSet if needed
	op, err := controllerutil.CreateOrPatch(ctx, r.Client, current, func() error {
		// Preserve labels and add ours
		if current.Labels == nil {
			current.Labels = make(map[string]string)
		}
		for k, v := range desired.Labels {
			current.Labels[k] = v
		}

		// Update spec
		current.Spec.Selector = desired.Spec.Selector
		current.Spec.Template = desired.Spec.Template

		return controllerutil.SetControllerReference(nfi, current, r.Scheme)
	})

	if err != nil {
		r.Recorder.Event(nfi, corev1.EventTypeWarning, ReasonDaemonSetFailed,
			fmt.Sprintf("Failed to update DaemonSet: %v", err))
		return nil, err
	}

	switch op {
	case controllerutil.OperationResultUpdated:
		r.Recorder.Event(nfi, corev1.EventTypeNormal, ReasonDaemonSetUpdated,
			fmt.Sprintf("DaemonSet %s updated", current.Name))
		log.Info("DaemonSet updated", "name", current.Name)
	}

	return current, nil
}

func (r *NodeFileInjectorReconciler) buildDaemonSet(nfi *filesv1alpha1.NodeFileInjector) *appsv1.DaemonSet {
	labels := map[string]string{
		"app.kubernetes.io/name":       "node-file-injector",
		"app.kubernetes.io/instance":   nfi.Name,
		"app.kubernetes.io/managed-by": "node-file-injector-controller",
	}

	// Build volume and volumeMount based on source
	var volumes []corev1.Volume
	var volumeMounts []corev1.VolumeMount
	var envVars []corev1.EnvVar

	if nfi.Spec.Source.ConfigMapKeyRef != nil {
		volumes = append(volumes, corev1.Volume{
			Name: "source",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: nfi.Spec.Source.ConfigMapKeyRef.Name,
					},
					Items: []corev1.KeyToPath{
						{
							Key:  nfi.Spec.Source.ConfigMapKeyRef.Key,
							Path: "content",
						},
					},
				},
			},
		})
		envVars = append(envVars, corev1.EnvVar{
			Name:  "SOURCE_TYPE",
			Value: "configmap",
		})
	} else if nfi.Spec.Source.SecretKeyRef != nil {
		volumes = append(volumes, corev1.Volume{
			Name: "source",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: nfi.Spec.Source.SecretKeyRef.Name,
					Items: []corev1.KeyToPath{
						{
							Key:  nfi.Spec.Source.SecretKeyRef.Key,
							Path: "content",
						},
					},
				},
			},
		})
		envVars = append(envVars, corev1.EnvVar{
			Name:  "SOURCE_TYPE",
			Value: "secret",
		})
	}

	volumeMounts = append(volumeMounts, corev1.VolumeMount{
		Name:      "source",
		MountPath: "/source",
		ReadOnly:  true,
	})

	// Add host path volume for the destination
	volumes = append(volumes, corev1.Volume{
		Name: "host",
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{
				Path: "/",
			},
		},
	})

	volumeMounts = append(volumeMounts, corev1.VolumeMount{
		Name:      "host",
		MountPath: "/host",
	})

	// Set default mode if not specified
	mode := nfi.Spec.Mode
	if mode == "" {
		mode = "0644"
	}

	// Set default owner/group if not specified
	owner := int64(0)
	if nfi.Spec.Owner != nil {
		owner = *nfi.Spec.Owner
	}
	group := int64(0)
	if nfi.Spec.Group != nil {
		group = *nfi.Spec.Group
	}

	// Environment variables for the injector
	envVars = append(envVars,
		corev1.EnvVar{Name: "TARGET_PATH", Value: nfi.Spec.Path},
		corev1.EnvVar{Name: "FILE_MODE", Value: mode},
		corev1.EnvVar{Name: "FILE_OWNER", Value: fmt.Sprintf("%d", owner)},
		corev1.EnvVar{Name: "FILE_GROUP", Value: fmt.Sprintf("%d", group)},
	)

	// Build the DaemonSet
	privileged := true
	runAsUser := int64(0)

	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("nfi-%s", nfi.Name),
			Namespace: nfi.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					NodeSelector: nfi.Spec.NodeSelector,
					Containers: []corev1.Container{
						{
							Name:  "file-injector",
							Image: "busybox:latest",
							Command: []string{
								"sh",
								"-c",
								fileInjectorScript,
							},
							Env:          envVars,
							VolumeMounts: volumeMounts,
							SecurityContext: &corev1.SecurityContext{
								Privileged: &privileged,
								RunAsUser:  &runAsUser,
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("10m"),
									corev1.ResourceMemory: resource.MustParse("32Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("64Mi"),
								},
							},
						},
					},
					Volumes:            volumes,
					HostNetwork:        false,
					HostPID:            false,
					ServiceAccountName: "default",
				},
			},
		},
	}

	return ds
}

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
		log.Error(err, "Failed to get latest resource for status update")
		return
	}

	// Copy status fields from working copy to latest version
	latestNFI.Status = nfi.Status

	if err := r.Status().Update(ctx, &latestNFI); err != nil {
		log.Error(err, "Failed to update status")
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeFileInjectorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&filesv1alpha1.NodeFileInjector{}).
		Owns(&appsv1.DaemonSet{}).
		Named("nodefileinjector").
		Complete(r)
}
