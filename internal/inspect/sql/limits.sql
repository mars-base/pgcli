-- Cluster-wide saturation gauges: current vs configured connections, and the
-- oldest transaction-id ages (wraparound risk). age(datfrozenxid) climbs toward
-- the ~2.1B wall past which Postgres refuses writes; a healthy cluster stays
-- well under autovacuum's 200M freeze trigger. mxid_age tracks multixact
-- wraparound toward the same wall. conn_used counts what max_connections
-- actually limits: client backends, excluding our own backend.
SELECT json_agg(t) FROM (
SELECT (SELECT count(*) FROM pg_stat_activity
          WHERE backend_type = 'client backend'
            AND pid <> pg_backend_pid())::int                       AS conn_used,
       current_setting('max_connections')::int                      AS conn_max,
       (SELECT max(age(datfrozenxid)) FROM pg_database)::bigint    AS max_xid_age,
       (SELECT max(mxid_age(datminmxid)) FROM pg_database)::bigint AS max_mxid_age
) t;
