/*
Copyright 2026.

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
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	scalingv1alpha1 "github.com/SebastienPeillet/celery-scale-operator/api/v1alpha1"
)

var _ = Describe("CeleryWorkerPool Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			resourceName      = "test-resource"
			resourceNamespace = "default"
			secretName        = "test-resource-dsn"
		)

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: resourceNamespace,
		}
		secretNamespacedName := types.NamespacedName{
			Name:      secretName,
			Namespace: resourceNamespace,
		}
		celeryworkerpool := &scalingv1alpha1.CeleryWorkerPool{}

		BeforeEach(func() {
			By("creating the Secret holding the database DSN")
			secret := &corev1.Secret{}
			err := k8sClient.Get(ctx, secretNamespacedName, secret)
			if err != nil && errors.IsNotFound(err) {
				secret = &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      secretName,
						Namespace: resourceNamespace,
					},
					// Deliberately unreachable: this suite exercises the error-handling
					// path (no Postgres available in envtest), not a real query.
					StringData: map[string]string{"dsn": "postgres://user:pass@127.0.0.1:1/db"},
				}
				Expect(k8sClient.Create(ctx, secret)).To(Succeed())
			}

			By("creating the custom resource for the Kind CeleryWorkerPool")
			err = k8sClient.Get(ctx, typeNamespacedName, celeryworkerpool)
			if err != nil && errors.IsNotFound(err) {
				resource := &scalingv1alpha1.CeleryWorkerPool{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: resourceNamespace,
					},
					Spec: scalingv1alpha1.CeleryWorkerPoolSpec{
						TargetDeploymentRef: "some-deployment",
						DatabaseSecretRef:   secretName,
						PollIntervalSeconds: 10,
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			By("Cleanup the specific resource instance CeleryWorkerPool")
			resource := &scalingv1alpha1.CeleryWorkerPool{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			By("Cleanup the Secret")
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, secretNamespacedName, secret)).To(Succeed())
			Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
		})

		It("reports a Ready=False condition when the database is unreachable", func() {
			By("Reconciling the created resource")
			controllerReconciler := &CeleryWorkerPoolReconciler{
				Client:  k8sClient,
				Scheme:  k8sClient.Scheme(),
				dbPools: make(map[types.NamespacedName]*dbPoolEntry),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).To(HaveOccurred())

			By("checking the Ready condition was set to False")
			updated := &scalingv1alpha1.CeleryWorkerPool{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		})
	})
})
