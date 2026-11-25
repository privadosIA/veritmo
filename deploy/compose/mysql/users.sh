#!/bin/sh
# Creates, and keeps up to date from .env, the two users of Grafana on the MySQL of the stack (run by the `mysql-users` service on every start):
#   grafana     all privileges on its own schema `grafana`, where Grafana keeps its state (there is no SQLite file of Grafana)
#   grafana_ro  SELECT on every table of the server's schema and nothing else: the history dashboard reads with it and cannot write
# Neither grant needs a table to exist, so this runs before the migration. Idempotent: running it again changes a password that changed in .env.
set -eu
root=$(cat /run/secrets/mysql_root_password)
gdb=$(cat /run/secrets/grafana_db_password)
gro=$(cat /run/secrets/grafana_ro_password)
db=${VERITMO_DB_NAME:-veritmo}
for v in "$gdb" "$gro" "$db"; do
    case "$v" in
        *[\'\"\\\`\;\ ]*|"") echo "users.sh: a password or name is empty or has a quote, a backslash, a backtick, a semicolon or a space" >&2; exit 1 ;;
    esac
done
mysql -h mysql -uroot -p"$root" <<SQL
CREATE DATABASE IF NOT EXISTS grafana CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
CREATE USER IF NOT EXISTS 'grafana'@'%' IDENTIFIED BY '$gdb';
ALTER USER 'grafana'@'%' IDENTIFIED BY '$gdb';
GRANT ALL PRIVILEGES ON grafana.* TO 'grafana'@'%';
CREATE USER IF NOT EXISTS 'grafana_ro'@'%' IDENTIFIED BY '$gro';
ALTER USER 'grafana_ro'@'%' IDENTIFIED BY '$gro';
REVOKE ALL PRIVILEGES, GRANT OPTION FROM 'grafana_ro'@'%';
GRANT SELECT ON \`$db\`.* TO 'grafana_ro'@'%';
FLUSH PRIVILEGES;
SQL
echo "users.sh: grafana and grafana_ro are in place"
