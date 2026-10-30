#!/usr/bin/env bash
set -euo pipefail
curl -sS -X POST http://127.0.0.1:18082/api/v1/admin/pairing
echo
