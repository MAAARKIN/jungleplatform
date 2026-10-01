#!/bin/bash
# Creates the dedicated integration-test database (runs on first container init).
set -e
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" <<-EOSQL
    CREATE DATABASE jungle_test OWNER jungle;
EOSQL