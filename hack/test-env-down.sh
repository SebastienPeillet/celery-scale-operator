#!/usr/bin/env bash
set -euo pipefail

NAMESPACE=default
DEPLOYMENT_NAME=celery-worker-test
SECRET_NAME=pgcondsn
PG_CONTAINER=celery-scale-test-pg
PG_PORT=55432

echo "==> Deleting CeleryWorkerPool sample CR"
kubectl -n "${NAMESPACE}" delete celeryworkerpool celeryworkerpool-sample --ignore-not-found

echo "==> Deleting test Deployment ${DEPLOYMENT_NAME}"
kubectl -n "${NAMESPACE}" delete deployment "${DEPLOYMENT_NAME}" --ignore-not-found

echo "==> Deleting Secret ${SECRET_NAME}"
kubectl -n "${NAMESPACE}" delete secret "${SECRET_NAME}" --ignore-not-found

echo "==> Removing Postgres test container (${PG_CONTAINER})"
docker rm -f "${PG_CONTAINER}" >/dev/null 2>&1 || true

echo "==> Test environment torn down."
