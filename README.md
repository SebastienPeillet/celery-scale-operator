# celery-scale-operator

A Kubernetes operator that prevents Celery worker pods from being killed mid-task during a scale-down.

## Description

By default, when a Kubernetes `Deployment` scales down, the ReplicaSet controller removes the **newest** pod — regardless of what that pod is doing. For long-running Celery tasks (sometimes over an hour), this means an in-progress task can be interrupted just because its pod happened to be created most recently, or at least the less busy pod will be removed.

`celery-scale-operator` fixes this without replacing your existing autoscaler (HPA/KEDA keep deciding *how many* replicas you need). It watches your Celery worker `Deployment` and periodically:

1. Queries your Celery result backend (`celery_taskmeta` table in Postgres) for tasks currently in `PROGRESS`, grouped by worker.
2. Matches each worker to the Kubernetes pod it's running in.
3. Sets the [`controller.kubernetes.io/pod-deletion-cost`](https://kubernetes.io/docs/concepts/workloads/controllers/replicaset/#pod-deletion-cost) annotation on each pod: idle pods get `0`, busy pods get their active task count.

Kubernetes natively honors this annotation when choosing which pod to remove on scale-down — pods with the **lowest** cost are removed first. The net effect: idle pods are always preferred for deletion over pods currently running a task.

## Prerequisites

- Access to a Kubernetes v1.11.3+ cluster.
- kubectl v1.11.3+.
- A Celery deployment using the **Postgres/SQLAlchemy result backend**, with [`result_extended = True`](https://docs.celeryq.dev/en/stable/userguide/configuration.html#result-extended) set — this is required for Celery to populate the `worker` column in `celery_taskmeta`, which the operator relies on to identify which pod is running which task.
- Celery workers running with their default hostname (`celery@<pod-name>`) — i.e. no custom `--hostname` override that would prevent matching a worker back to its pod by name.
- Go v1.26+ and Docker, only if you intend to build the image yourself instead of using a published one.

## Roadmap

- Add more Celery backend resolver 

## The `CeleryWorkerPool` custom resource

One `CeleryWorkerPool` object manages one target `Deployment`.

| Spec field | Type | Required | Description |
|---|---|---|---|
| `targetDeploymentRef` | string | yes | Name of the `Deployment` whose pods are managed (used to verify pod ownership, not to control replica count). |
| `databaseSecretRef` | string | yes | Name of a `Secret`, in the same namespace, holding the Postgres connection string (see below). |
| `labelSelector` | map[string]string | no | Labels used to find the worker pods. Must match the pods of `targetDeploymentRef`. If omitted, matches all pods in the namespace. |
| `pollIntervalSeconds` | int32 | no (default `15`) | How often to re-check activity and refresh the annotations. |
| `dryRun` | bool | no (default `false`) | When `true`, computes and logs what would be patched without actually modifying any pod. |

| Status field | Description |
|---|---|
| `conditions` | A single `Ready` condition — `True`/`ReconcileSucceeded` on a successful pass, `False`/`ReconcileFailed` with the error message otherwise. |
| `lastReconcileTime` | Timestamp of the last reconcile attempt. If this stops advancing, the controller isn't running or is stuck. |
| `managedPods` | Number of pods currently matched by `labelSelector` and owned by `targetDeploymentRef`. |

### Required Secret

The `Secret` referenced by `databaseSecretRef` must contain a single key, `dsn`, holding a full Postgres connection string:

```sh
kubectl create secret generic celery-pg-dsn \
  --from-literal=dsn='postgres://<user>:<password>@<host>:5432/<database>?sslmode=require'
```

### Example

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: celery-pg-dsn
type: Opaque
stringData:
  dsn: postgres://celery:changeme@postgres.example.svc:5432/celery?sslmode=require
---
apiVersion: scaling.hytech-imaging.fr/v1alpha1
kind: CeleryWorkerPool
metadata:
  name: celery-worker-pool
spec:
  targetDeploymentRef: celery-worker
  databaseSecretRef: celery-pg-dsn
  labelSelector:
    app: celery-worker
  pollIntervalSeconds: 15
  dryRun: false
```

## Installing on your cluster

### Option A — using a published image

Images are published to `ghcr.io/sebastienpeillet/celery-scale-operator` on every GitHub release.

```sh
make deploy IMG=ghcr.io/sebastienpeillet/celery-scale-operator:<tag>
```

This installs the CRD and deploys the controller (with its RBAC and ServiceAccount) to whichever cluster your current `kubectl` context points to.

### Option B — building your own image

```sh
make docker-build IMG=<your-registry>/celery-scale-operator:<tag>
make docker-push IMG=<your-registry>/celery-scale-operator:<tag>
make deploy IMG=<your-registry>/celery-scale-operator:<tag>
```

> **NOTE**: If you encounter RBAC errors deploying, you may need cluster-admin privileges — `make deploy` creates a Namespace, CRDs, ClusterRole/ClusterRoleBinding and a Deployment.

### Applying a `CeleryWorkerPool`

Create your `Secret` and `CeleryWorkerPool` (see the example above), then:

```sh
kubectl apply -f your-celeryworkerpool.yaml
```

### Verifying it's working

```sh
kubectl get celeryworkerpool <name> -o yaml   # check status.conditions (Ready) and status.managedPods
kubectl get pod <worker-pod> -o jsonpath='{.metadata.annotations.controller\.kubernetes\.io/pod-deletion-cost}'
```

## To Uninstall

**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete celeryworkerpool --all -A
```

**Delete the APIs (CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Local development

This project was scaffolded with [kubebuilder](https://book.kubebuilder.io/). The usual inner loop:

```sh
make manifests generate   # after changing api/v1alpha1 types or +kubebuilder:rbac markers
make install              # install/refresh the CRD on your dev cluster
make run                  # run the manager locally against your current kubeconfig
```

`hack/test-env-up.sh` spins up a disposable end-to-end test fixture on a kind local cluster (a local Postgres container with a `celery_taskmeta` table, a test `Deployment`, a `Secret`, and a sample `CeleryWorkerPool` with one pod marked busy) — handy for exercising the controller without a real Celery deployment. Tear it down with `hack/test-env-down.sh`.

```sh
DRY_RUN=true ./hack/test-env-up.sh
make run
# ...observe logs, then flip to real mode:
kubectl patch celeryworkerpool celeryworkerpool-sample --type=merge -p '{"spec":{"dryRun":false}}'
./hack/test-env-down.sh
```

Run the unit test suite with:

```sh
make test
```

## Project Distribution

Following the options to release and provide this solution to the users.

### By providing a bundle with all YAML files

A consolidated `install.yaml` (CRD + RBAC + controller Deployment) is built and attached
as a release asset automatically by CI on every published GitHub release — it isn't
committed to the repository (see `.github/workflows/release.yml`).

Users can just run 'kubectl apply -f <URL for YAML BUNDLE>' to install
the project, i.e.:

```sh
kubectl apply -f https://github.com/SebastienPeillet/celery-scale-operator/releases/download/<tag>/install.yaml
```

## Contributing

This is a personal learning project (Go + Kubernetes operators). Issues and pull requests are welcome.

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

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
