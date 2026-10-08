#!/bin/bash
# Wraps the PostgreSQL image's own entrypoint, pointing pg_cron at the database the
# container serves. Other commands run unchanged.
set -e

if [ "$1" = postgres ]; then
    set -- "$@" -c "cron.database_name=${POSTGRES_DB:-postgres}"
fi

exec /usr/local/bin/docker-entrypoint.sh "$@"
