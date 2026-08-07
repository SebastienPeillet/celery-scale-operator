#!/usr/bin/env bash
set -euo pipefail

NAMESPACE=default
DEPLOYMENT_NAME=celery-worker-test
SECRET_NAME=pgcondsn
LABEL_KEY=app
LABEL_VALUE=celery-worker-test
PG_CONTAINER=celery-scale-test-pg
PG_PASSWORD=test
PG_DB=celery
PG_PORT=55432
DRY_RUN=${DRY_RUN:-true}

echo "==> Starting test Postgres container (${PG_CONTAINER})"
docker rm -f "${PG_CONTAINER}" >/dev/null 2>&1 || true
docker run -d --name "${PG_CONTAINER}" \
  -e POSTGRES_PASSWORD="${PG_PASSWORD}" \
  -e POSTGRES_DB="${PG_DB}" \
  -p ${PG_PORT}:5432 \
  postgres:16 >/dev/null

echo "==> Waiting for Postgres to accept connections"
until docker exec "${PG_CONTAINER}" pg_isready -U postgres -d "${PG_DB}" >/dev/null 2>&1; do
  sleep 1
done
# pg_isready can report ready during the brief internal restart postgres does
# right after initdb, before the custom POSTGRES_DB is reachable; give it a
# moment to settle and confirm with a real query.
for i in $(seq 1 15); do
  if docker exec "${PG_CONTAINER}" psql -U postgres -d "${PG_DB}" -c 'select 1' >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

echo "==> Creating celery_taskmeta table"
docker exec -i "${PG_CONTAINER}" psql -U postgres -d "${PG_DB}" >/dev/null <<'SQL'
CREATE TABLE IF NOT EXISTS celery_taskmeta (
    id SERIAL PRIMARY KEY,
    task_id VARCHAR(255),
    status VARCHAR(50),
    worker VARCHAR(255)
);
TRUNCATE celery_taskmeta;
SQL

DSN="postgres://postgres:${PG_PASSWORD}@localhost:${PG_PORT}/${PG_DB}?sslmode=disable"

echo "==> Creating Kubernetes Secret ${SECRET_NAME}"
kubectl -n "${NAMESPACE}" delete secret "${SECRET_NAME}" --ignore-not-found >/dev/null
kubectl -n "${NAMESPACE}" create secret generic "${SECRET_NAME}" --from-literal=dsn="${DSN}" >/dev/null

echo "==> Creating test Deployment ${DEPLOYMENT_NAME} (10 replicas)"
kubectl -n "${NAMESPACE}" delete deployment "${DEPLOYMENT_NAME}" --ignore-not-found >/dev/null
kubectl -n "${NAMESPACE}" apply -f - >/dev/null <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${DEPLOYMENT_NAME}
  namespace: ${NAMESPACE}
spec:
  replicas: 10
  selector:
    matchLabels:
      ${LABEL_KEY}: ${LABEL_VALUE}
  template:
    metadata:
      labels:
        ${LABEL_KEY}: ${LABEL_VALUE}
    spec:
      containers:
      - name: sleep
        image: busybox
        command: ["sleep", "infinity"]
EOF

echo "==> Waiting for pods to be Ready"
kubectl -n "${NAMESPACE}" wait --for=condition=Ready pod -l ${LABEL_KEY}=${LABEL_VALUE} --timeout=90s >/dev/null

PODS=($(kubectl -n "${NAMESPACE}" get pods -l ${LABEL_KEY}=${LABEL_VALUE} -o jsonpath='{.items[*].metadata.name}'))
BUSY_POD=${PODS[0]}

echo "==> Marking pod ${BUSY_POD} as busy (status=PROGRESS) in celery_taskmeta; others stay idle"
docker exec -i "${PG_CONTAINER}" psql -U postgres -d "${PG_DB}" >/dev/null <<SQL
INSERT INTO celery_taskmeta (task_id, status, worker) VALUES ('test-task-1', 'PROGRESS', 'celery@${BUSY_POD}');
SQL

echo "==> Applying CeleryWorkerPool sample CR (dryRun=${DRY_RUN})"
cat > /tmp/celeryworkerpool-test.yaml <<EOF
apiVersion: scaling.hytech-imaging.fr/v1alpha1
kind: CeleryWorkerPool
metadata:
  name: celeryworkerpool-sample
  namespace: ${NAMESPACE}
spec:
  targetDeploymentRef: ${DEPLOYMENT_NAME}
  databaseSecretRef: ${SECRET_NAME}
  labelSelector:
    ${LABEL_KEY}: ${LABEL_VALUE}
  pollIntervalSeconds: 10
  dryRun: ${DRY_RUN}
EOF
kubectl apply -f /tmp/celeryworkerpool-test.yaml >/dev/null

echo ""
echo "==> Test environment ready."
echo "    Busy pod (expected cost=1): ${BUSY_POD}"
echo "    Idle pods (expected cost=0): all others
echo ""
echo "Now run: make run"
