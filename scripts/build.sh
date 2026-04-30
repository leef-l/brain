#!/bin/bash
# 限制 go build 资源：1 核 CPU + 1.5G 内存
exec systemd-run --quiet --scope \
  -p MemoryMax=1536M \
  -p CPUQuota=100% \
  go build "$@"
