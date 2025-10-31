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
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"strconv"

	filesv1alpha1 "github.com/guilhem/node-file-injector/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

//go:embed scripts/file-injector-oneshot.sh
var fileInjectorOneShotScript string

const (
	// Condition reasons for Job operations
	ReasonJobsCreated  = "JobsCreated"
	ReasonJobsUpdated  = "JobsUpdated"
	ReasonJobsFailed   = "JobsFailed"
	ReasonAllSucceeded = "AllSucceeded"
	ReasonSomeFailed   = "SomeFailed"

	// Hash length for node names (8 characters provides good uniqueness)
	nodeHashLength = 8

	// Label keys for Job management
	labelNFIName  = "files.barpilot.io/nfi-name"
	labelNodeName = "files.barpilot.io/node-name"

	// Annotation keys
	annotationGeneration = "files.barpilot.io/generation"
)

// hashNodeName creates a short hash of the node name for use in labels and names
// to avoid exceeding Kubernetes' 63 character limit
func hashNodeName(nodeName string) string {
	hash := sha256.Sum256([]byte(nodeName))
	return hex.EncodeToString(hash[:])[:nodeHashLength]
}

// hashNodeNameWithGen creates a short hash including both node name and generation
// This ensures each generation creates a unique Job name
func hashNodeNameWithGen(nodeName string, generation int64) string {
	data := fmt.Sprintf("%s-%d", nodeName, generation)
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])[:nodeHashLength]
}

// JobDeployer implements the Deployer interface for Job-based deployments
type JobDeployer struct {
	client   client.Client
	scheme   *runtime.Scheme
	recorder record.EventRecorder
}

// NewJobDeployer creates a new Job deployer
func NewJobDeployer(c client.Client, scheme *runtime.Scheme, recorder record.EventRecorder) Deployer {
	return &JobDeployer{
		client:   c,
		scheme:   scheme,
		recorder: recorder,
	}
}

// Reconcile creates or updates Jobs for file injection
func (j *JobDeployer) Reconcile(ctx context.Context, nfi *filesv1alpha1.NodeFileInjector) (DeploymentStatus, error) {
	log := logf.FromContext(ctx)
	status := DeploymentStatus{
		NodeStatuses: make(map[string]filesv1alpha1.NodeExecutionStatus),
	}

	// List matching nodes
	nodeList := &corev1.NodeList{}
	listOpts := []client.ListOption{}
	if len(nfi.Spec.NodeSelector) > 0 {
		listOpts = append(listOpts, client.MatchingLabels(nfi.Spec.NodeSelector))
	}
	if err := j.client.List(ctx, nodeList, listOpts...); err != nil {
		return status, fmt.Errorf("failed to list nodes: %w", err)
	}
	status.NodesMatched = int32(len(nodeList.Items))

	if len(nodeList.Items) == 0 {
		log.Info("No nodes match the selector")
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             ReasonAllSucceeded,
			Message:            "No nodes to process",
			ObservedGeneration: nfi.Generation,
		})
		return status, nil
	}

	// Reconcile Jobs for each node
	successCount := 0
	failedCount := 0
	runningCount := 0
	pendingCount := 0

	for _, node := range nodeList.Items {
		jobStatus, err := j.reconcileNodeJob(ctx, nfi, node.Name)
		if err != nil {
			log.Error(err, "Failed to reconcile job for node", "node", node.Name)
			failedCount++
			status.NodeStatuses[node.Name] = filesv1alpha1.NodeExecutionStatus{
				Status:             "Failed",
				LastTransitionTime: &metav1.Time{Time: metav1.Now().Time},
			}
			continue
		}

		status.NodeStatuses[node.Name] = jobStatus

		// Count by status
		switch jobStatus.Status {
		case "Succeeded":
			successCount++
		case "Failed":
			failedCount++
		case "Running":
			runningCount++
		case "Pending":
			pendingCount++
		}
	}

	// Set overall condition based on job statuses
	totalNodes := len(nodeList.Items)
	if successCount == totalNodes {
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             ReasonAllSucceeded,
			Message:            fmt.Sprintf("All %d nodes processed successfully", totalNodes),
			ObservedGeneration: nfi.Generation,
		})
	} else if failedCount > 0 {
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             ReasonSomeFailed,
			Message:            fmt.Sprintf("%d/%d nodes succeeded, %d failed, %d running, %d pending", successCount, totalNodes, failedCount, runningCount, pendingCount),
			ObservedGeneration: nfi.Generation,
		})
	} else {
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "Progressing",
			Message:            fmt.Sprintf("%d/%d nodes succeeded, %d running, %d pending", successCount, totalNodes, runningCount, pendingCount),
			ObservedGeneration: nfi.Generation,
		})
	}

	return status, nil
}

// reconcileNodeJob reconciles a single Job for a specific node
func (j *JobDeployer) reconcileNodeJob(ctx context.Context, nfi *filesv1alpha1.NodeFileInjector, nodeName string) (filesv1alpha1.NodeExecutionStatus, error) {
	log := logf.FromContext(ctx)

	// Use hashed node name + generation to keep job name under 63 characters
	// Including generation ensures each generation creates a unique Job
	nodeHash := hashNodeNameWithGen(nodeName, nfi.Generation)
	jobName := fmt.Sprintf("nfi-%s-%s", nfi.Name, nodeHash)

	// Ensure job name doesn't exceed 63 characters
	if len(jobName) > 63 {
		maxNFINameLen := 63 - len("nfi-") - len(nodeHash) - 1
		truncatedName := nfi.Name
		if len(truncatedName) > maxNFINameLen {
			truncatedName = truncatedName[:maxNFINameLen]
		}
		jobName = fmt.Sprintf("nfi-%s-%s", truncatedName, nodeHash)
	}

	// Build desired Job
	desired := j.buildJob(nfi, nodeName)

	// Get existing Job
	existing := &batchv1.Job{}
	err := j.client.Get(ctx, client.ObjectKey{
		Name:      jobName,
		Namespace: nfi.Namespace,
	}, existing)

	if err != nil {
		if !apierrors.IsNotFound(err) {
			return filesv1alpha1.NodeExecutionStatus{}, err
		}

		// Create new Job
		if err := controllerutil.SetControllerReference(nfi, desired, j.scheme); err != nil {
			return filesv1alpha1.NodeExecutionStatus{}, err
		}

		if err := j.client.Create(ctx, desired); err != nil {
			j.recorder.Eventf(nfi, corev1.EventTypeWarning, ReasonJobsFailed,
				"Failed to create Job for node %s: %v", nodeName, err)
			return filesv1alpha1.NodeExecutionStatus{}, err
		}

		log.Info("Job created", "job", jobName, "node", nodeName)
		j.recorder.Eventf(nfi, corev1.EventTypeNormal, ReasonJobsCreated,
			"Job created for node %s", nodeName)

		return filesv1alpha1.NodeExecutionStatus{
			JobName:            jobName,
			Status:             "Pending",
			LastTransitionTime: &metav1.Time{Time: metav1.Now().Time},
		}, nil
	}

	// Check if Job needs recreation (generation changed)
	existingGen := existing.Annotations[annotationGeneration]
	currentGen := strconv.FormatInt(nfi.Generation, 10)

	if existingGen != currentGen {
		log.Info("Job generation changed, recreating", "job", jobName, "oldGen", existingGen, "newGen", currentGen)

		// Delete old Job
		if err := j.client.Delete(ctx, existing, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil && !apierrors.IsNotFound(err) {
			return filesv1alpha1.NodeExecutionStatus{}, err
		}

		// Create new Job
		if err := controllerutil.SetControllerReference(nfi, desired, j.scheme); err != nil {
			return filesv1alpha1.NodeExecutionStatus{}, err
		}

		if err := j.client.Create(ctx, desired); err != nil {
			j.recorder.Eventf(nfi, corev1.EventTypeWarning, ReasonJobsFailed,
				"Failed to recreate Job for node %s: %v", nodeName, err)
			return filesv1alpha1.NodeExecutionStatus{}, err
		}

		log.Info("Job recreated", "job", jobName, "node", nodeName)
		j.recorder.Eventf(nfi, corev1.EventTypeNormal, ReasonJobsUpdated,
			"Job recreated for node %s (generation %s)", nodeName, currentGen)

		return filesv1alpha1.NodeExecutionStatus{
			JobName:            jobName,
			Status:             "Pending",
			LastTransitionTime: &metav1.Time{Time: metav1.Now().Time},
		}, nil
	}

	// Return status from existing Job
	return j.getJobStatus(existing), nil
}

// getJobStatus extracts status from a Job
func (j *JobDeployer) getJobStatus(job *batchv1.Job) filesv1alpha1.NodeExecutionStatus {
	status := filesv1alpha1.NodeExecutionStatus{
		JobName: job.Name,
		Status:  "Pending",
	}

	// Check Job conditions
	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue {
			status.Status = "Succeeded"
			status.LastTransitionTime = &condition.LastTransitionTime
			return status
		}
		if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
			status.Status = "Failed"
			status.LastTransitionTime = &condition.LastTransitionTime
			return status
		}
	}

	// Check if Job is running
	if job.Status.Active > 0 {
		status.Status = "Running"
		if job.Status.StartTime != nil {
			status.LastTransitionTime = job.Status.StartTime
		}
	}

	return status
}

// Delete removes all Jobs for this NodeFileInjector
func (j *JobDeployer) Delete(ctx context.Context, nfi *filesv1alpha1.NodeFileInjector) error {
	log := logf.FromContext(ctx)

	// List all Jobs owned by this NFI
	jobList := &batchv1.JobList{}
	labelSelector := labels.SelectorFromSet(labels.Set{
		labelNFIName: nfi.Name,
	})
	listOpts := []client.ListOption{
		client.InNamespace(nfi.Namespace),
		client.MatchingLabelsSelector{Selector: labelSelector},
	}

	if err := j.client.List(ctx, jobList, listOpts...); err != nil {
		return err
	}

	// Delete each Job
	for i := range jobList.Items {
		job := &jobList.Items[i]
		if err := j.client.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil {
			if !apierrors.IsNotFound(err) {
				log.Error(err, "Failed to delete job", "job", job.Name)
				return err
			}
		}
		log.Info("Job deleted", "job", job.Name)
	}

	return nil
}

func (j *JobDeployer) buildJob(nfi *filesv1alpha1.NodeFileInjector, nodeName string) *batchv1.Job {
	// Hash node name + generation to keep job name under 63 characters
	// Format: nfi-{nfiName}-{nodeHash} where hash includes generation
	// This ensures each generation creates a unique Job
	nodeHash := hashNodeNameWithGen(nodeName, nfi.Generation)
	jobName := fmt.Sprintf("nfi-%s-%s", nfi.Name, nodeHash)

	// Ensure job name doesn't exceed 63 characters
	if len(jobName) > 63 {
		// If still too long, truncate nfi name and use full hash
		maxNFINameLen := 63 - len("nfi-") - len(nodeHash) - 1
		truncatedName := nfi.Name
		if len(truncatedName) > maxNFINameLen {
			truncatedName = truncatedName[:maxNFINameLen]
		}
		jobName = fmt.Sprintf("nfi-%s-%s", truncatedName, nodeHash)
	}

	jobLabels := map[string]string{
		"app.kubernetes.io/name":       "node-file-injector",
		"app.kubernetes.io/instance":   nfi.Name,
		"app.kubernetes.io/component":  "file-injection-job",
		"app.kubernetes.io/managed-by": "node-file-injector-controller",
		labelNFIName:                   nfi.Name,
		labelNodeName:                  nodeHash, // Use hash for label value
	}

	annotations := map[string]string{
		annotationGeneration: strconv.FormatInt(nfi.Generation, 10),
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

	// Start from user template (or empty template if not provided)
	var ttlSeconds *int32
	var backoffLimit *int32
	var activeDeadlineSeconds *int64
	var podTemplate corev1.PodTemplateSpec

	if nfi.Spec.JobTemplate != nil {
		ttlSeconds = nfi.Spec.JobTemplate.TTLSecondsAfterFinished
		backoffLimit = nfi.Spec.JobTemplate.BackoffLimit
		activeDeadlineSeconds = nfi.Spec.JobTemplate.ActiveDeadlineSeconds

		if nfi.Spec.JobTemplate.Template != nil {
			// Use user template as base
			podTemplate = *nfi.Spec.JobTemplate.Template.DeepCopy()
		}
	}

	// Initialize podTemplate metadata if needed
	if podTemplate.Labels == nil {
		podTemplate.Labels = make(map[string]string)
	}
	if podTemplate.Annotations == nil {
		podTemplate.Annotations = make(map[string]string)
	}

	// Add required labels (user labels take precedence if already set)
	for k, v := range jobLabels {
		if _, exists := podTemplate.Labels[k]; !exists {
			podTemplate.Labels[k] = v
		}
	}

	// Ensure NodeSelector exists and add required hostname selector
	if podTemplate.Spec.NodeSelector == nil {
		podTemplate.Spec.NodeSelector = make(map[string]string)
	}
	podTemplate.Spec.NodeSelector["kubernetes.io/hostname"] = nodeName

	// Set RestartPolicy if not specified
	if podTemplate.Spec.RestartPolicy == "" {
		podTemplate.Spec.RestartPolicy = corev1.RestartPolicyOnFailure
	}

	// Find or create the file-injector container
	fileInjectorIndex := -1
	for i, container := range podTemplate.Spec.Containers {
		if container.Name == "file-injector" {
			fileInjectorIndex = i
			break
		}
	}

	privileged := true
	runAsUser := int64(0)

	if fileInjectorIndex == -1 {
		// Container doesn't exist, create it with defaults
		fileInjectorContainer := corev1.Container{
			Name:  "file-injector",
			Image: "busybox:latest",
			Command: []string{
				"sh",
				"-c",
				fileInjectorOneShotScript,
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
		}
		podTemplate.Spec.Containers = append([]corev1.Container{fileInjectorContainer}, podTemplate.Spec.Containers...)
	} else {
		// Container exists, inject required fields
		container := &podTemplate.Spec.Containers[fileInjectorIndex]

		// Set required fields (override user values for critical settings)
		container.Command = []string{"sh", "-c", fileInjectorOneShotScript}
		container.Env = envVars
		container.VolumeMounts = volumeMounts

		// Ensure security context is set and privileged
		if container.SecurityContext == nil {
			container.SecurityContext = &corev1.SecurityContext{}
		}
		container.SecurityContext.Privileged = &privileged
		container.SecurityContext.RunAsUser = &runAsUser

		// Set defaults for optional fields if not provided by user
		if container.Image == "" {
			container.Image = "busybox:latest"
		}
		if container.Resources.Requests == nil && container.Resources.Limits == nil {
			container.Resources = corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("10m"),
					corev1.ResourceMemory: resource.MustParse("32Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
				},
			}
		}
	}

	// Add required volumes if not already present
	existingVolumes := make(map[string]bool)
	for _, v := range podTemplate.Spec.Volumes {
		existingVolumes[v.Name] = true
	}

	for _, v := range volumes {
		if !existingVolumes[v.Name] {
			podTemplate.Spec.Volumes = append(podTemplate.Spec.Volumes, v)
		}
	}

	// Set default ServiceAccountName if not specified
	if podTemplate.Spec.ServiceAccountName == "" {
		podTemplate.Spec.ServiceAccountName = "default"
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        jobName,
			Namespace:   nfi.Namespace,
			Labels:      jobLabels,
			Annotations: annotations,
		},
		Spec: batchv1.JobSpec{
			TTLSecondsAfterFinished: ttlSeconds,
			BackoffLimit:            backoffLimit,
			ActiveDeadlineSeconds:   activeDeadlineSeconds,
			Template:                podTemplate,
		},
	}

	return job
}
