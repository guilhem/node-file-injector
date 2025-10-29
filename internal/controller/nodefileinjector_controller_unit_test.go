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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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

	Context("buildDaemonSet", func() {
		It("should build DaemonSet with ConfigMap source", func() {
			nfi := &filesv1alpha1.NodeFileInjector{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nfi",
					Namespace: "default",
				},
				Spec: filesv1alpha1.NodeFileInjectorSpec{
					Path: "/etc/test/config.yaml",
					Source: filesv1alpha1.SourceReference{
						ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-config",
							},
							Key: "config.yaml",
						},
					},
					Mode:  "0644",
					Owner: func() *int64 { i := int64(0); return &i }(),
					Group: func() *int64 { i := int64(0); return &i }(),
				},
			}

			ds := reconciler.buildDaemonSet(nfi)

			Expect(ds.Name).To(Equal("nfi-test-nfi"))
			Expect(ds.Namespace).To(Equal("default"))
			Expect(ds.Labels).To(HaveKeyWithValue("app.kubernetes.io/name", "node-file-injector"))
			Expect(ds.Labels).To(HaveKeyWithValue("app.kubernetes.io/instance", "test-nfi"))

			// Verify volumes
			Expect(ds.Spec.Template.Spec.Volumes).To(HaveLen(2)) // source + host-path

			// Find source volume
			var sourceVolume *corev1.Volume
			for i := range ds.Spec.Template.Spec.Volumes {
				if ds.Spec.Template.Spec.Volumes[i].Name == "source" {
					sourceVolume = &ds.Spec.Template.Spec.Volumes[i]
					break
				}
			}
			Expect(sourceVolume).NotTo(BeNil())
			Expect(sourceVolume.ConfigMap).NotTo(BeNil())
			Expect(sourceVolume.ConfigMap.Name).To(Equal("test-config"))

			// Verify container
			Expect(ds.Spec.Template.Spec.Containers).To(HaveLen(1))
			container := ds.Spec.Template.Spec.Containers[0]
			Expect(container.Name).To(Equal("file-injector"))

			// Verify environment variables
			envMap := make(map[string]string)
			for _, env := range container.Env {
				envMap[env.Name] = env.Value
			}
			Expect(envMap["TARGET_PATH"]).To(Equal("/etc/test/config.yaml"))
			Expect(envMap["FILE_MODE"]).To(Equal("0644"))
			Expect(envMap["FILE_OWNER"]).To(Equal("0"))
			Expect(envMap["FILE_GROUP"]).To(Equal("0"))
		})

		It("should build DaemonSet with Secret source", func() {
			nfi := &filesv1alpha1.NodeFileInjector{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nfi-secret",
					Namespace: "default",
				},
				Spec: filesv1alpha1.NodeFileInjectorSpec{
					Path: "/etc/secrets/token",
					Source: filesv1alpha1.SourceReference{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-secret",
							},
							Key: "token",
						},
					},
					Mode:  "0600",
					Owner: func() *int64 { i := int64(1000); return &i }(),
					Group: func() *int64 { i := int64(1000); return &i }(),
				},
			}

			ds := reconciler.buildDaemonSet(nfi)

			// Find source volume
			var sourceVolume *corev1.Volume
			for i := range ds.Spec.Template.Spec.Volumes {
				if ds.Spec.Template.Spec.Volumes[i].Name == "source" {
					sourceVolume = &ds.Spec.Template.Spec.Volumes[i]
					break
				}
			}
			Expect(sourceVolume).NotTo(BeNil())
			Expect(sourceVolume.Secret).NotTo(BeNil())
			Expect(sourceVolume.Secret.SecretName).To(Equal("test-secret"))

			// Verify environment variables for permissions
			container := ds.Spec.Template.Spec.Containers[0]
			envMap := make(map[string]string)
			for _, env := range container.Env {
				envMap[env.Name] = env.Value
			}
			Expect(envMap["FILE_MODE"]).To(Equal("0600"))
			Expect(envMap["FILE_OWNER"]).To(Equal("1000"))
			Expect(envMap["FILE_GROUP"]).To(Equal("1000"))
		})

		It("should apply nodeSelector when specified", func() {
			nfi := &filesv1alpha1.NodeFileInjector{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nfi-selector",
					Namespace: "default",
				},
				Spec: filesv1alpha1.NodeFileInjectorSpec{
					Path: "/etc/test/config",
					Source: filesv1alpha1.SourceReference{
						ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-config",
							},
							Key: "config",
						},
					},
					NodeSelector: map[string]string{
						"node-role.kubernetes.io/worker": "true",
						"zone":                           "us-east-1a",
					},
				},
			}

			ds := reconciler.buildDaemonSet(nfi)

			Expect(ds.Spec.Template.Spec.NodeSelector).To(HaveKeyWithValue("node-role.kubernetes.io/worker", "true"))
			Expect(ds.Spec.Template.Spec.NodeSelector).To(HaveKeyWithValue("zone", "us-east-1a"))
		})

		It("should set proper security context", func() {
			nfi := &filesv1alpha1.NodeFileInjector{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nfi-security",
					Namespace: "default",
				},
				Spec: filesv1alpha1.NodeFileInjectorSpec{
					Path: "/etc/test/config",
					Source: filesv1alpha1.SourceReference{
						ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-config",
							},
							Key: "config",
						},
					},
				},
			}

			ds := reconciler.buildDaemonSet(nfi)

			container := ds.Spec.Template.Spec.Containers[0]

			// Verify security context
			Expect(container.SecurityContext).NotTo(BeNil())
			Expect(*container.SecurityContext.Privileged).To(BeTrue())
			Expect(*container.SecurityContext.RunAsUser).To(Equal(int64(0)))

			// Verify resource limits are set
			Expect(container.Resources.Limits).NotTo(BeEmpty())
			Expect(container.Resources.Requests).NotTo(BeEmpty())

			cpuLimit := container.Resources.Limits[corev1.ResourceCPU]
			Expect(cpuLimit.Cmp(resource.MustParse("100m"))).To(Equal(0))

			cpuRequest := container.Resources.Requests[corev1.ResourceCPU]
			Expect(cpuRequest.Cmp(resource.MustParse("10m"))).To(Equal(0))

			memLimit := container.Resources.Limits[corev1.ResourceMemory]
			Expect(memLimit.Cmp(resource.MustParse("64Mi"))).To(Equal(0))

			memRequest := container.Resources.Requests[corev1.ResourceMemory]
			Expect(memRequest.Cmp(resource.MustParse("32Mi"))).To(Equal(0))
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
