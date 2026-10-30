#!/usr/bin/env bash
set -euo pipefail
limit="${1:-20}"
curl -sS "http://127.0.0.1:18082/api/v1/admin/jobs?limit=${limit}" | python3 -m json.tool
