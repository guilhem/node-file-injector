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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	filesv1alpha1 "github.com/guilhem/node-file-injector/api/v1alpha1"
)

var _ = Describe("NodeFileInjector Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default", // TODO(user):Modify as needed
		}
		nodefileinjector := &filesv1alpha1.NodeFileInjector{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind NodeFileInjector")
			err := k8sClient.Get(ctx, typeNamespacedName, nodefileinjector)
			if err != nil && errors.IsNotFound(err) {
				resource := &filesv1alpha1.NodeFileInjector{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: filesv1alpha1.NodeFileInjectorSpec{
						Path: "/tmp/test-file.conf",
						Source: filesv1alpha1.SourceReference{
							ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "test-configmap",
								},
								Key: "test-key",
							},
						},
						FileMode: "0644",
						Owner:    func() *int64 { i := int64(0); return &i }(),
						Group:    func() *int64 { i := int64(0); return &i }(),
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			// TODO(user): Cleanup logic after each test, like removing the resource instance.
			resource := &filesv1alpha1.NodeFileInjector{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance NodeFileInjector")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &NodeFileInjectorReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(10),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify that the resource has been processed
			resource := &filesv1alpha1.NodeFileInjector{}
			err = k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			// Verify finalizer was added
			Expect(resource.Finalizers).To(ContainElement(nodeFileInjectorFinalizer))

			// Verify status conditions have been initialized
			Expect(resource.Status.Conditions).NotTo(BeEmpty())
		})
	})
})
