# Grafana dashboards

There are two, with different data:

- **`veritmo-dashboard.json`** (**MySQL** data source): the run history, read directly from the database. See below.
- **`veritmo-machine.json`** (**Prometheus** data source): the machine, live: VRAM, utilization, temperature and power draw of each card, RAM, disk, music services, runs per state and duration (p50 and p95). It is fed by the server's `GET /api/metrics`; how to add it to Prometheus is in [SERVER.md](../SERVER.md#metrics-for-prometheus). It is imported the same way (Dashboards → New → Import) and asks you to pick the Prometheus data source. Both are generated with `python3 docs/grafana/build_dashboard.py history|machine`.

## Grafana on the run history

A Grafana dashboard that reads the history tables in MySQL directly (see "Settings: the database of the run history" in [SERVER.md](../SERVER.md)). It needs nothing from the orchestrator: you can keep Grafana off and start it only when you want to look.

## What it shows

Runs per period, succeeded and failed, average duration; runs per day and state; duration per server (to compare one server with another); VRAM used per card during the runs; the measured speed of each card; the slowest runs and the latest failures. The **Server** selector limits all panels to one or several servers.

## Installing it

1. **A read-only user** in your MySQL, only on the three tables the dashboard uses (it cannot see the events or the revisions):

   ```sql
   CREATE USER 'grafana_ro'@'%' IDENTIFIED BY '<a password>';
   GRANT SELECT ON veritmo.runs TO 'grafana_ro'@'%';
   GRANT SELECT ON veritmo.run_samples TO 'grafana_ro'@'%';
   GRANT SELECT ON veritmo.calibration_samples TO 'grafana_ro'@'%';
   ```

2. In Grafana: **Connections → Data sources → Add → MySQL** (host `ip:3306`, database `veritmo`, that user and its password) and *Save & test*.
3. **Dashboards → New → Import**, upload `veritmo-dashboard.json` and pick that data source in "Veritmo MySQL".

The JSON files are generated with `python3 docs/grafana/build_dashboard.py history > docs/grafana/veritmo-dashboard.json` and `python3 docs/grafana/build_dashboard.py machine > docs/grafana/veritmo-machine.json`; edit the script, not the JSON.

## With the Compose stack

[The Compose stack](../COMPOSE.md) does all of the above by itself: its `mysql-users` step creates the user (`grafana_ro`) and gives it `SELECT` on **every table of the server's schema** (not only the three above: Grafana in the stack reads from the whole database, and the user still cannot write or drop anything), the `Veritmo MySQL` and Prometheus data sources are provisioned with the fixed ids `mysql` and `prometheus`, and both dashboards of this folder are loaded as they are (their `${DS_MYSQL}` and `${DS_PROMETHEUS}` inputs are filled in as the files are copied). Grafana keeps its own state in the same MySQL, in a schema of its own named `grafana`.

## Limits

- The VRAM data in this dashboard only exists **while a run is in progress** (the server samples then). To see the whole machine, always on, use the machine dashboard (Prometheus), above.
- The queries were tested against a MySQL 8.4 with test data; the column names are those of the `runs`, `run_samples` and `calibration_samples` tables.
