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

package deployer

import (
	"context"
	_ "embed"
	"fmt"

	filesv1alpha1 "github.com/guilhem/node-file-injector/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

//go:embed scripts/file-injector.sh
var fileInjectorScript string

const (
	// Condition reasons for DaemonSet operations
	ReasonDaemonSetCreated = "DaemonSetCreated"
	ReasonDaemonSetUpdated = "DaemonSetUpdated"
	ReasonDaemonSetFailed  = "DaemonSetFailed"
	ReasonSucceeded        = "Succeeded"
	ReasonFailed           = "Failed"
)

// DaemonSetDeployer implements the Deployer interface for DaemonSet-based deployments
type DaemonSetDeployer struct {
	client   client.Client
	scheme   *runtime.Scheme
	recorder record.EventRecorder
}

// NewDaemonSetDeployer creates a new DaemonSet deployer
func NewDaemonSetDeployer(c client.Client, scheme *runtime.Scheme, recorder record.EventRecorder) Deployer {
	return &DaemonSetDeployer{
		client:   c,
		scheme:   scheme,
		recorder: recorder,
	}
}

// Reconcile creates or updates the DaemonSet for file injection
func (d *DaemonSetDeployer) Reconcile(ctx context.Context, nfi *filesv1alpha1.NodeFileInjector) (DeploymentStatus, error) {
	log := logf.FromContext(ctx)
	status := DeploymentStatus{
		NodeStatuses: make(map[string]filesv1alpha1.NodeExecutionStatus),
	}

	// Count matching nodes
	nodeList := &corev1.NodeList{}
	listOpts := []client.ListOption{}
	if len(nfi.Spec.NodeSelector) > 0 {
		listOpts = append(listOpts, client.MatchingLabels(nfi.Spec.NodeSelector))
	}
	if err := d.client.List(ctx, nodeList, listOpts...); err != nil {
		return status, fmt.Errorf("failed to list nodes: %w", err)
	}
	status.NodesMatched = int32(len(nodeList.Items))

	// Build desired DaemonSet
	desired := d.buildDaemonSet(nfi)
	status.DaemonSetName = desired.Name

	// Get current DaemonSet
	current := &appsv1.DaemonSet{}
	err := d.client.Get(ctx, types.NamespacedName{
		Name:      desired.Name,
		Namespace: desired.Namespace,
	}, current)

	if err != nil {
		if !apierrors.IsNotFound(err) {
			return status, err
		}

		// Create DaemonSet
		if err := controllerutil.SetControllerReference(nfi, desired, d.scheme); err != nil {
			return status, err
		}

		if err := d.client.Create(ctx, desired); err != nil {
			d.recorder.Event(nfi, corev1.EventTypeWarning, ReasonDaemonSetFailed,
				fmt.Sprintf("Failed to create DaemonSet: %v", err))
			meta.SetStatusCondition(&status.Conditions, metav1.Condition{
				Type:               "Ready",
				Status:             metav1.ConditionFalse,
				Reason:             ReasonDaemonSetFailed,
				Message:            fmt.Sprintf("Failed to create DaemonSet: %v", err),
				ObservedGeneration: nfi.Generation,
			})
			return status, err
		}

		d.recorder.Event(nfi, corev1.EventTypeNormal, ReasonDaemonSetCreated,
			fmt.Sprintf("DaemonSet %s created", desired.Name))
		log.Info("DaemonSet created", "name", desired.Name)

		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             ReasonSucceeded,
			Message:            "DaemonSet created successfully",
			ObservedGeneration: nfi.Generation,
		})
		return status, nil
	}

	// Update DaemonSet if needed
	op, err := controllerutil.CreateOrPatch(ctx, d.client, current, func() error {
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

		return controllerutil.SetControllerReference(nfi, current, d.scheme)
	})

	if err != nil {
		d.recorder.Event(nfi, corev1.EventTypeWarning, ReasonDaemonSetFailed,
			fmt.Sprintf("Failed to update DaemonSet: %v", err))
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             ReasonDaemonSetFailed,
			Message:            fmt.Sprintf("Failed to update DaemonSet: %v", err),
			ObservedGeneration: nfi.Generation,
		})
		return status, err
	}

	switch op {
	case controllerutil.OperationResultUpdated:
		d.recorder.Event(nfi, corev1.EventTypeNormal, ReasonDaemonSetUpdated,
			fmt.Sprintf("DaemonSet %s updated", current.Name))
		log.Info("DaemonSet updated", "name", current.Name)
	}

	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             ReasonSucceeded,
		Message:            "DaemonSet reconciled successfully",
		ObservedGeneration: nfi.Generation,
	})

	return status, nil
}

// Delete removes the DaemonSet
func (d *DaemonSetDeployer) Delete(ctx context.Context, nfi *filesv1alpha1.NodeFileInjector) error {
	log := logf.FromContext(ctx)

	dsName := fmt.Sprintf("nfi-%s", nfi.Name)
	ds := &appsv1.DaemonSet{}
	err := d.client.Get(ctx, types.NamespacedName{
		Name:      dsName,
		Namespace: nfi.Namespace,
	}, ds)

	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil // Already deleted
		}
		return err
	}

	if err := d.client.Delete(ctx, ds); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
	}

	log.Info("DaemonSet deleted", "name", dsName)
	return nil
}

func (d *DaemonSetDeployer) buildDaemonSet(nfi *filesv1alpha1.NodeFileInjector) *appsv1.DaemonSet {
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

	// Set default file mode if not specified
	fileMode := nfi.Spec.FileMode
	if fileMode == "" {
		fileMode = "0644"
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
		corev1.EnvVar{Name: "FILE_MODE", Value: fileMode},
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
