#!/bin/bash

export TEST_REDIS_SECONDARIES=$(terraform output -json secondaries | gojq 'join(":6379,") + ":6379"' | tr -d \")
export TEST_REDIS_PRIMARY_HOST=$(terraform output -raw primary_host)
export TEST_REDIS_PRIMARY_PORT=6379
export TEST_REDIS_TLS=true
export CA_CERT_FILE=$PWD/data/ca.crt
export TLS_CERT_FILE=$PWD/data/tls.crt
export TLS_KEY_FILE=$PWD/data/tls.key

unset TEST_REDIS_CLUSTER TEST_REDIS_SENTINELS TEST_REDIS_SENTINEL_MASTER_NAME
