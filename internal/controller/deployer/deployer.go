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

	filesv1alpha1 "github.com/guilhem/node-file-injector/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Deployer is the interface for deploying file injectors to nodes
type Deployer interface {
	// Reconcile creates or updates the deployment for file injection
	Reconcile(ctx context.Context, nfi *filesv1alpha1.NodeFileInjector) (DeploymentStatus, error)

	// Delete removes the deployment
	Delete(ctx context.Context, nfi *filesv1alpha1.NodeFileInjector) error
}

// DeploymentStatus represents the status returned by a deployer
type DeploymentStatus struct {
	// NodesMatched is the number of nodes that match the selector
	NodesMatched int32

	// NodeStatuses tracks per-node execution status (only for Job mode)
	NodeStatuses map[string]filesv1alpha1.NodeExecutionStatus

	// Conditions are the status conditions to set
	Conditions []metav1.Condition

	// DaemonSetName is the name of the DaemonSet (only for DaemonSet mode)
	DaemonSetName string
}

// NewDeployer creates a deployer based on the deployment mode
func NewDeployer(mode filesv1alpha1.DeploymentMode, c client.Client, scheme *runtime.Scheme, recorder record.EventRecorder) Deployer {
	switch mode {
	case filesv1alpha1.DeploymentModeJob:
		return NewJobDeployer(c, scheme, recorder)
	case filesv1alpha1.DeploymentModeDaemonSet:
		fallthrough
	default:
		return NewDaemonSetDeployer(c, scheme, recorder)
	}
}
