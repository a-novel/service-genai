#!/bin/bash
# Wraps the PostgreSQL image's own entrypoint, pointing pg_cron at the database the
# container serves. Other commands run unchanged.
set -e

# The default follows the official entrypoint, which names the database after POSTGRES_USER when
# POSTGRES_DB is unset. Any other default would point pg_cron at a database the init SQL never runs in.
if [ "$1" = postgres ]; then
    set -- "$@" -c "cron.database_name=${POSTGRES_DB:-${POSTGRES_USER:-postgres}}"
fi

exec /usr/local/bin/docker-entrypoint.sh "$@"
