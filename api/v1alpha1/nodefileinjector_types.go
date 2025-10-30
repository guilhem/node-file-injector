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

package v1alpha1

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeploymentMode defines how files are deployed to nodes
// +kubebuilder:validation:Enum=daemonset;job
type DeploymentMode string

const (
	// DeploymentModeDaemonSet uses a DaemonSet for continuous file monitoring and updates
	DeploymentModeDaemonSet DeploymentMode = "daemonset"
	// DeploymentModeJob uses Jobs for one-time file injection per node
	DeploymentModeJob DeploymentMode = "job"
)

// JobTemplate defines a template for Jobs used in job mode deployment.
// This allows full customization of the Job and Pod specifications.
type JobTemplate struct {
	// TTLSecondsAfterFinished limits the lifetime of a Job that has finished
	// execution (either Complete or Failed). If this field is set, after the Job finishes,
	// it is eligible to be automatically deleted. If this field is unset,
	// the Job won't be automatically deleted.
	// +optional
	TTLSecondsAfterFinished *int32 `json:"ttlSecondsAfterFinished,omitempty"`

	// BackoffLimit specifies the number of retries before marking this job failed.
	// If not set, uses the default value from Kubernetes (6).
	// +optional
	BackoffLimit *int32 `json:"backoffLimit,omitempty"`

	// ActiveDeadlineSeconds specifies the duration in seconds relative to the startTime
	// that the job may be active before the system tries to terminate it.
	// +optional
	ActiveDeadlineSeconds *int64 `json:"activeDeadlineSeconds,omitempty"`

	// Template describes the pod that will be created when executing a job.
	// The controller will merge this template with the required file-injector container.
	// You can use this to specify resource limits, tolerations, node affinity, etc.
	// +optional
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Template *corev1.PodTemplateSpec `json:"template,omitempty"`
}

// NodeExecutionStatus represents the status of file injection on a specific node
type NodeExecutionStatus struct {
	// JobName is the name of the Job managing this node
	// +optional
	JobName string `json:"jobName,omitempty"`

	// Status represents the current state of the Job
	// Possible values: Pending, Running, Succeeded, Failed
	// +optional
	Status string `json:"status,omitempty"`

	// LastTransitionTime is the last time the status transitioned
	// +optional
	LastTransitionTime *metav1.Time `json:"lastTransitionTime,omitempty"`
}

// SourceReference defines a reference to a ConfigMap or Secret key
type SourceReference struct {
	// ConfigMapKeyRef is a reference to a ConfigMapKeyRef in the same namespace
	// +optional
	ConfigMapKeyRef *corev1.ConfigMapKeySelector `json:"configMapKeyRef,omitempty"`

	// SecretKeyRef is a reference to a SecretKeyRef in the same namespace
	// +optional
	SecretKeyRef *corev1.SecretKeySelector `json:"secretKeyRef,omitempty"`
}

// NodeFileInjectorSpec defines the desired state of NodeFileInjector
type NodeFileInjectorSpec struct {
	// Mode determines how files are deployed to nodes.
	// - "daemonset": Uses a DaemonSet for continuous monitoring and updates (default)
	// - "job": Uses Jobs for one-time file injection per node
	// +kubebuilder:validation:Enum=daemonset;job
	// +kubebuilder:default=daemonset
	// +optional
	Mode DeploymentMode `json:"mode,omitempty"`

	// JobTemplate provides optional configuration for Job-based deployments.
	// Only applicable when Mode is "job". If not specified, default values are used.
	// The template allows full customization of Job and Pod specifications including
	// resources, tolerations, affinity, security context, etc.
	// +optional
	JobTemplate *JobTemplate `json:"jobTemplate,omitempty"`

	// NodeSelector selects the nodes where the file should be injected.
	// If empty, the file will be injected on all nodes.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Source defines the source of the file content (ConfigMap or Secret).
	// Exactly one of ConfigMap or Secret must be specified.
	// +required
	Source SourceReference `json:"source"`

	// Path is the absolute path on the node where the file should be written.
	// The directory will be created if it doesn't exist.
	// +kubebuilder:validation:Pattern="^/.*$"
	// +kubebuilder:validation:MinLength=1
	// +required
	Path string `json:"path"`

	// FileMode is the file mode (permissions) in octal format (e.g., "0644").
	// +kubebuilder:validation:Pattern="^0[0-7]{3}$"
	// +kubebuilder:default="0644"
	// +optional
	FileMode string `json:"fileMode,omitempty"`

	// Owner is the user ID that should own the file.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=0
	// +optional
	Owner *int64 `json:"owner,omitempty"`

	// Group is the group ID that should own the file.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=0
	// +optional
	Group *int64 `json:"group,omitempty"`
}

// NodeFileInjectorStatus defines the observed state of NodeFileInjector.
type NodeFileInjectorStatus struct {
	// Conditions represent the current state of the NodeFileInjector resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// DaemonSetName is the name of the managed DaemonSet.
	// Only populated when Mode is "daemonset".
	// +optional
	DaemonSetName string `json:"daemonSetName,omitempty"`

	// NodeStatus tracks the execution status of each node when Mode is "job".
	// The key is the node name, and the value contains the Job name and status.
	// +optional
	NodeStatus map[string]NodeExecutionStatus `json:"nodeStatus,omitempty"`

	// NodesMatched is the number of nodes that match the node selector.
	// +optional
	NodesMatched int32 `json:"nodesMatched,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=nfi
// +kubebuilder:printcolumn:name="Path",type=string,JSONPath=`.spec.path`
// +kubebuilder:printcolumn:name="Nodes Matched",type=integer,JSONPath=`.status.nodesMatched`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NodeFileInjector is the Schema for the nodefileinjectors API
type NodeFileInjector struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of NodeFileInjector
	// +required
	Spec NodeFileInjectorSpec `json:"spec"`

	// status defines the observed state of NodeFileInjector
	// +optional
	Status NodeFileInjectorStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// NodeFileInjectorList contains a list of NodeFileInjector
type NodeFileInjectorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeFileInjector `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NodeFileInjector{}, &NodeFileInjectorList{})
}

// ValidateSource validates that exactly one source is specified
func (spec *NodeFileInjectorSpec) ValidateSource() error {
	if spec.Source.ConfigMapKeyRef == nil && spec.Source.SecretKeyRef == nil {
		return fmt.Errorf("either configMap or secret must be specified in source")
	}
	if spec.Source.ConfigMapKeyRef != nil && spec.Source.SecretKeyRef != nil {
		return fmt.Errorf("only one of configMap or secret can be specified in source")
	}
	return nil
}
