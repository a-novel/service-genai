# A postgres image with the extensions the service needs pre-loaded at build time.
#
# It does not run the service's schema migrations; run the migrations target separately.
FROM docker.io/library/postgres:18.4

ARG DEBIAN_FRONTEND=noninteractive

# Require password authentication for local connections; PostgreSQL 18 already defaults host
# authentication to SCRAM.
ENV POSTGRES_INITDB_ARGS=--auth=scram-sha-256

# ======================================================================================================================
# Install pg_cron.
# ======================================================================================================================
# pg_cron runs the retention purge (see builds/database.sql). It cannot load without
# shared_preload_libraries, and it is set on the conf sample so the init-time server that runs
# database.sql has it too, not just the running server.
RUN apt-get update \
  && apt-get install -y --no-install-recommends postgresql-18-cron=1.6.7-3.pgdg13+1 \
  && rm -rf /var/lib/apt/lists/* \
  && echo "shared_preload_libraries='pg_cron'" >> /usr/share/postgresql/postgresql.conf.sample

# ======================================================================================================================
# Prepare extension scripts.
# ======================================================================================================================
# Points cron.database_name at the served database.
COPY ./builds/database.entrypoint.sh /usr/local/bin/database.entrypoint.sh
RUN chmod +x /usr/local/bin/database.entrypoint.sh

# Runs once on an empty data directory, to create the extensions.
COPY ./builds/database.sql /docker-entrypoint-initdb.d/init.sql

# ======================================================================================================================
# Finish setup.
# ======================================================================================================================
EXPOSE 5432

# Postgres does not provide a healthcheck by default.
HEALTHCHECK --interval=1s --timeout=5s --retries=10 --start-period=1s \
  CMD pg_isready || exit 1

ENTRYPOINT ["/usr/local/bin/database.entrypoint.sh"]

# Restore the original command from the base image.
CMD ["postgres"]
