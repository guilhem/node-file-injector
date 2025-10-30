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
	"github.com/guilhem/node-file-injector/internal/controller/deployer"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"

	filesv1alpha1 "github.com/guilhem/node-file-injector/api/v1alpha1"
)

var _ = Describe("NodeFileInjector Unit Tests", func() {
	var reconciler *NodeFileInjectorReconciler

	BeforeEach(func() {
		reconciler = &NodeFileInjectorReconciler{
			Scheme: scheme.Scheme,
		}
	})

	Context("Deployer Factory", func() {
		It("should create DaemonSet deployer for daemonset mode", func() {
			d := deployer.NewDeployer(filesv1alpha1.DeploymentModeDaemonSet, nil, scheme.Scheme, nil)
			Expect(d).NotTo(BeNil())
		})

		It("should create Job deployer for job mode", func() {
			d := deployer.NewDeployer(filesv1alpha1.DeploymentModeJob, nil, scheme.Scheme, nil)
			Expect(d).NotTo(BeNil())
		})

		It("should default to DaemonSet deployer for empty mode", func() {
			d := deployer.NewDeployer("", nil, scheme.Scheme, nil)
			Expect(d).NotTo(BeNil())
		})
	})

	Context("ValidateSource", func() {
		It("should accept valid ConfigMap source", func() {
			spec := filesv1alpha1.NodeFileInjectorSpec{
				Path: "/test",
				Source: filesv1alpha1.SourceReference{
					ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: "test-config",
						},
						Key: "config",
					},
				},
			}

			err := spec.ValidateSource()
			Expect(err).NotTo(HaveOccurred())
		})

		It("should accept valid Secret source", func() {
			spec := filesv1alpha1.NodeFileInjectorSpec{
				Path: "/test",
				Source: filesv1alpha1.SourceReference{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: "test-secret",
						},
						Key: "token",
					},
				},
			}

			err := spec.ValidateSource()
			Expect(err).NotTo(HaveOccurred())
		})

		It("should reject when both ConfigMap and Secret are specified", func() {
			spec := filesv1alpha1.NodeFileInjectorSpec{
				Path: "/test",
				Source: filesv1alpha1.SourceReference{
					ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: "test-config",
						},
						Key: "config",
					},
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: "test-secret",
						},
						Key: "token",
					},
				},
			}

			err := spec.ValidateSource()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("only one"))
		})

		It("should reject when neither ConfigMap nor Secret is specified", func() {
			spec := filesv1alpha1.NodeFileInjectorSpec{
				Path:   "/test",
				Source: filesv1alpha1.SourceReference{},
			}

			err := spec.ValidateSource()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("either"))
		})
	})

	Context("setCondition", func() {
		It("should set a new condition", func() {
			nfi := &filesv1alpha1.NodeFileInjector{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nfi",
					Namespace: "default",
				},
			}

			reconciler.setCondition(nfi, ConditionReady, metav1.ConditionTrue, ReasonSucceeded, "All good")

			Expect(nfi.Status.Conditions).To(HaveLen(1))
			Expect(nfi.Status.Conditions[0].Type).To(Equal(ConditionReady))
			Expect(nfi.Status.Conditions[0].Status).To(Equal(metav1.ConditionTrue))
			Expect(nfi.Status.Conditions[0].Reason).To(Equal(ReasonSucceeded))
			Expect(nfi.Status.Conditions[0].Message).To(Equal("All good"))
		})

		It("should update existing condition", func() {
			nfi := &filesv1alpha1.NodeFileInjector{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nfi",
					Namespace: "default",
				},
				Status: filesv1alpha1.NodeFileInjectorStatus{
					Conditions: []metav1.Condition{
						{
							Type:    ConditionReady,
							Status:  metav1.ConditionFalse,
							Reason:  ReasonFailed,
							Message: "Pending",
						},
					},
				},
			}

			reconciler.setCondition(nfi, ConditionReady, metav1.ConditionTrue, ReasonSucceeded, "Now ready")

			Expect(nfi.Status.Conditions).To(HaveLen(1))
			Expect(nfi.Status.Conditions[0].Status).To(Equal(metav1.ConditionTrue))
			Expect(nfi.Status.Conditions[0].Reason).To(Equal(ReasonSucceeded))
			Expect(nfi.Status.Conditions[0].Message).To(Equal("Now ready"))
		})

		It("should not duplicate conditions", func() {
			nfi := &filesv1alpha1.NodeFileInjector{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nfi",
					Namespace: "default",
				},
			}

			reconciler.setCondition(nfi, ConditionReady, metav1.ConditionTrue, ReasonSucceeded, "Ready")
			reconciler.setCondition(nfi, ConditionReady, metav1.ConditionFalse, ReasonFailed, "Failed")
			reconciler.setCondition(nfi, ConditionReady, metav1.ConditionTrue, ReasonSucceeded, "Ready again")

			Expect(nfi.Status.Conditions).To(HaveLen(1))
		})
	})
})
