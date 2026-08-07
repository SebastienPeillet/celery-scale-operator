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
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	scalingv1alpha1 "github.com/SebastienPeillet/celery-scale-operator/api/v1alpha1"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// CeleryWorkerPoolReconciler reconciles a CeleryWorkerPool object
type CeleryWorkerPoolReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	dbPoolsMu sync.Mutex
	dbPools   map[types.NamespacedName]*dbPoolEntry
}

type dbPoolEntry struct {
	dsn string
	db  *sql.DB
}

// +kubebuilder:rbac:groups=scaling.hytech-imaging.fr,resources=celeryworkerpools,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=scaling.hytech-imaging.fr,resources=celeryworkerpools/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=scaling.hytech-imaging.fr,resources=celeryworkerpools/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=replicasets,verbs=get
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the CeleryWorkerPool object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.24.1/pkg/reconcile
func (r *CeleryWorkerPoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, reconcileErr error) {
	logger := logf.FromContext(ctx)

	// Get Pool
	var pool scalingv1alpha1.CeleryWorkerPool
	err := r.Get(ctx, req.NamespacedName, &pool)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	var nbManagedPods int32
	defer func() {
		r.SetCondition(&pool, nbManagedPods, reconcileErr)
		if statusErr := r.Status().Update(ctx, &pool); statusErr != nil {
			logger.Error(statusErr, "failed to update CeleryWorkerPool status")
			if reconcileErr == nil {
				reconcileErr = statusErr
			}
		}
	}()

	// Get backend connection
	db, err := r.getDBForPool(ctx, &pool)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Get worker activity from backend
	workerMapActivity, err := activeTasksByWorker(ctx, db)
	if err != nil {
		return ctrl.Result{}, err
	}

	// List worker pods
	workerPods, err := r.listManagedPods(ctx, &pool)
	if err != nil {
		return ctrl.Result{}, err
	}
	nbManagedPods = int32(len(workerPods))

	// Patch pod-deletion-cost in pods
	_, err = r.patchPodsDeletionCost(
		ctx, workerPods, workerMapActivity, pool.Spec.DryRun,
	)
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: time.Duration(pool.Spec.PollIntervalSeconds) * time.Second}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *CeleryWorkerPoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.dbPools = make(map[types.NamespacedName]*dbPoolEntry)
	return ctrl.NewControllerManagedBy(mgr).
		For(&scalingv1alpha1.CeleryWorkerPool{}).
		Named("celeryworkerpool").
		Complete(r)
}

// SetCondition updates pool status condition
func (r *CeleryWorkerPoolReconciler) SetCondition(pool *scalingv1alpha1.CeleryWorkerPool, managedPods int32, reconcileErr error) {
	pool.Status.ManagedPods = managedPods
	pool.Status.LastReconcileTime = &metav1.Time{Time: time.Now()}

	cond := metav1.Condition{Type: "Ready", ObservedGeneration: pool.Generation}
	if reconcileErr == nil {
		cond.Status, cond.Reason, cond.Message = metav1.ConditionTrue, "ReconcileSucceeded", fmt.Sprintf("%d pods managed", managedPods)
	} else {
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, "ReconcileFailed", reconcileErr.Error()
	}
	meta.SetStatusCondition(&pool.Status.Conditions, cond)
}

func (r *CeleryWorkerPoolReconciler) getDBForPool(ctx context.Context, pool *scalingv1alpha1.CeleryWorkerPool) (*sql.DB, error) {
	r.dbPoolsMu.Lock()
	defer r.dbPoolsMu.Unlock()

	var secret corev1.Secret
	namespacedName := types.NamespacedName{Namespace: pool.Namespace, Name: pool.Spec.DatabaseSecretRef}
	err := r.Get(ctx, namespacedName, &secret) // get secret
	if err != nil {
		return nil, fmt.Errorf("fetching secret: %w", err)
	}

	dsnBytes, ok := secret.Data["dsn"]
	if !ok {
		return nil, fmt.Errorf("no 'dsn' key in secret")
	}
	connectionString := string(dsnBytes)

	entry, ok := r.dbPools[namespacedName]

	if ok && entry.dsn == connectionString {
		return entry.db, nil
	} else if ok && entry.dsn != connectionString {
		err = entry.db.Close()
		if err != nil {
			return nil, fmt.Errorf("close connection failed: %w", err)
		}
	}
	db, err := sql.Open("pgx", connectionString)
	if err != nil {
		return nil, fmt.Errorf("connection failed: %w", err)
	}
	r.dbPools[namespacedName] = &dbPoolEntry{dsn: connectionString, db: db}

	return db, err
}

func activeTasksByWorker(ctx context.Context, db *sql.DB) (results map[string]int32, err error) {
	logger := logf.FromContext(ctx)
	rows, err := db.QueryContext(
		ctx,
		"SELECT worker, COUNT(*) AS active_tasks FROM celery_taskmeta WHERE status = 'PROGRESS' GROUP BY worker",
	)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() {
		err := rows.Close()
		if err != nil {
			results = nil
		}
	}()

	results = make(map[string]int32)
	var found bool
	for rows.Next() {
		var worker string
		var activeTasks int32
		if err := rows.Scan(&worker, &activeTasks); err != nil {
			return results, err
		}
		_, worker, found = strings.Cut(worker, "@")
		if !found {
			logger.Info("worker name has no separator, ignoring", "worker", worker)
			continue
		}
		results[worker] = activeTasks
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating rows: %w", err)
	}

	return results, err
}

func (r *CeleryWorkerPoolReconciler) listManagedPods(ctx context.Context, pool *scalingv1alpha1.CeleryWorkerPool) ([]corev1.Pod, error) {
	var podList corev1.PodList

	err := r.List(ctx, &podList, client.InNamespace(pool.Namespace), client.MatchingLabels(pool.Spec.LabelSelector))
	if err != nil {
		return nil, fmt.Errorf("listing pods failed: %w", err)
	}

	var finalPodList []corev1.Pod
	for _, pod := range podList.Items {
		toMonitor, err := r.isOwnedByTargetDeployment(ctx, &pod, pool)
		if err != nil {
			return nil, err
		}
		if toMonitor {
			finalPodList = append(finalPodList, pod)
		}
	}

	return finalPodList, err
}

func (r *CeleryWorkerPoolReconciler) isOwnedByTargetDeployment(ctx context.Context, pod *corev1.Pod, pool *scalingv1alpha1.CeleryWorkerPool) (bool, error) {
	rsRef := findControllerRef(pod.OwnerReferences, "ReplicaSet")
	if rsRef == nil {
		return false, nil // pod sans ReplicaSet parent (ex: pod créé à la main) -> pas géré
	}

	var rs appsv1.ReplicaSet
	if err := r.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: rsRef.Name}, &rs); err != nil {
		return false, fmt.Errorf("fetching replicaset %s: %w", rsRef.Name, err)
	}

	deployRef := findControllerRef(rs.OwnerReferences, "Deployment")
	return deployRef != nil && deployRef.Name == pool.Spec.TargetDeploymentRef, nil
}

func findControllerRef(refs []metav1.OwnerReference, kind string) *metav1.OwnerReference {
	for i := range refs {
		if refs[i].Kind == kind && refs[i].Controller != nil && *refs[i].Controller {
			return &refs[i]
		}
	}
	return nil
}

func (r *CeleryWorkerPoolReconciler) patchPodsDeletionCost(ctx context.Context, pods []corev1.Pod, workerMapActivity map[string]int32, dryRun bool) (bool, error) {
	for i := range pods {
		cost := deletionCostFor(pods[i].Name, workerMapActivity)
		err := r.patchPodDeletionCost(ctx, pods[i], cost, dryRun)
		if err != nil {
			return false, err
		}
	}
	return true, nil
}

// deletionCostFor returns the pod-deletion-cost value for a pod: its active
// task count if known, 0 (idle, safe to delete first) otherwise.
func deletionCostFor(podName string, workerMapActivity map[string]int32) int32 {
	if cost, ok := workerMapActivity[podName]; ok {
		return cost
	}
	return 0
}

func (r *CeleryWorkerPoolReconciler) patchPodDeletionCost(ctx context.Context, pod corev1.Pod, cost int32, dryRun bool) error {
	logf.FromContext(ctx).Info("setting pod-deletion-cost", "pod", pod.Name, "cost", cost, "dryRun", dryRun)
	if dryRun {
		return nil
	}
	patch := fmt.Appendf(nil, `{"metadata":{"annotations":{"controller.kubernetes.io/pod-deletion-cost": "%d" }}}`, cost)
	return r.Patch(ctx, &pod, client.RawPatch(types.StrategicMergePatchType, patch))
}
