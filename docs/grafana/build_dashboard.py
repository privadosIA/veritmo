#!/usr/bin/env python3
"""Generates the two Grafana dashboards. Edit it here and regenerate:
  python3 docs/grafana/build_dashboard.py history > docs/grafana/veritmo-dashboard.json   (MySQL data source)
  python3 docs/grafana/build_dashboard.py machine > docs/grafana/veritmo-machine.json     (Prometheus data source)"""
import json
import sys

DS = {"type": "mysql", "uid": "${DS_MYSQL}"}
SERVER = "r.server IN (${server:singlequote})"


def target(sql, fmt="time_series", ref="A"):
    return {"datasource": DS, "editorMode": "code", "format": fmt, "rawQuery": True, "rawSql": sql, "refId": ref}


def panel(pid, title, kind, x, y, w, h, sql, fmt="time_series", desc="", options=None, defaults=None, overrides=None):
    p = {
        "id": pid, "title": title, "type": kind, "description": desc,
        "gridPos": {"x": x, "y": y, "w": w, "h": h}, "datasource": DS,
        "targets": [target(sql, fmt)],
        "fieldConfig": {"defaults": defaults or {}, "overrides": overrides or []},
        "options": options or {},
    }
    return p


panels = []
panels.append(panel(1, "Runs in the period", "stat", 0, 0, 4, 4,
    f"SELECT COUNT(*) AS runs FROM runs r WHERE $__timeFilter(r.created_at) AND {SERVER}", "table"))
panels.append(panel(2, "Succeeded", "stat", 4, 0, 4, 4,
    f"SELECT COUNT(*) AS succeeded FROM runs r WHERE $__timeFilter(r.created_at) AND r.state='succeeded' AND {SERVER}", "table",
    defaults={"color": {"mode": "fixed", "fixedColor": "green"}}))
panels.append(panel(3, "Failed", "stat", 8, 0, 4, 4,
    f"SELECT COUNT(*) AS failed FROM runs r WHERE $__timeFilter(r.created_at) AND r.state IN ('failed','interrupted') AND {SERVER}", "table",
    defaults={"color": {"mode": "fixed", "fixedColor": "red"}}))
panels.append(panel(4, "Average duration (s)", "stat", 12, 0, 4, 4,
    f"SELECT AVG(TIMESTAMPDIFF(SECOND, r.started_at, r.finished_at)) AS seconds FROM runs r WHERE $__timeFilter(r.created_at) AND r.state='succeeded' AND r.started_at IS NOT NULL AND {SERVER}", "table",
    defaults={"unit": "s", "decimals": 0}))
panels.append(panel(5, "Runs per server", "barchart", 16, 0, 8, 4,
    f"SELECT COALESCE(NULLIF(r.server,''),'(unnamed)') AS server, COUNT(*) AS runs FROM runs r WHERE $__timeFilter(r.created_at) AND {SERVER} GROUP BY 1 ORDER BY 2 DESC", "table",
    options={"orientation": "horizontal", "legend": {"showLegend": False}}))
panels.append(panel(6, "Runs per day and state", "timeseries", 0, 4, 12, 8,
    f"SELECT $__timeGroupAlias(r.created_at,'1d'), r.state AS metric, COUNT(*) AS value FROM runs r WHERE $__timeFilter(r.created_at) AND {SERVER} GROUP BY 1,2 ORDER BY 1",
    desc="Counts the runs created each day, by state.",
    defaults={"custom": {"drawStyle": "bars", "fillOpacity": 70, "stacking": {"mode": "normal"}}, "decimals": 0},
    overrides=[
        {"matcher": {"id": "byName", "options": "succeeded"}, "properties": [{"id": "color", "value": {"mode": "fixed", "fixedColor": "green"}}]},
        {"matcher": {"id": "byName", "options": "failed"}, "properties": [{"id": "color", "value": {"mode": "fixed", "fixedColor": "red"}}]},
        {"matcher": {"id": "byName", "options": "interrupted"}, "properties": [{"id": "color", "value": {"mode": "fixed", "fixedColor": "orange"}}]},
    ]))
panels.append(panel(7, "Average duration per server (s)", "timeseries", 12, 4, 12, 8,
    f"SELECT $__timeGroupAlias(r.created_at,'1d'), COALESCE(NULLIF(r.server,''),'(unnamed)') AS metric, AVG(TIMESTAMPDIFF(SECOND, r.started_at, r.finished_at)) AS value FROM runs r WHERE $__timeFilter(r.created_at) AND r.state='succeeded' AND r.started_at IS NOT NULL AND {SERVER} GROUP BY 1,2 ORDER BY 1",
    desc="Succeeded runs only. Useful to compare one server with another.",
    defaults={"unit": "s", "custom": {"drawStyle": "points", "pointSize": 8}}))
panels.append(panel(8, "VRAM used per card (MB, maximum per minute)", "timeseries", 0, 12, 24, 9,
    f"SELECT $__timeGroupAlias(s.ts,'1m'), CONCAT(COALESCE(NULLIF(r.server,''),'?'), ' · GPU ', s.gpu) AS metric, MAX(s.vram_used_mb) AS value FROM run_samples s JOIN runs r ON r.id = s.run_id WHERE $__timeFilter(s.ts) AND {SERVER} GROUP BY 1,2 ORDER BY 1",
    desc="The server samples it while a run is in progress (every few seconds). There is no data outside runs: to see the machine while it is always on you need /metrics for Prometheus.",
    defaults={"unit": "decmbytes", "custom": {"lineWidth": 1, "fillOpacity": 10}}))
panels.append(panel(9, "Measured speed of each card (1 = the reference card, 2 = twice as slow)", "timeseries", 0, 21, 12, 8,
    "SELECT $__timeGroupAlias(c.`at`,'1d'), CONCAT(COALESCE(NULLIF(c.server,''),'?'), ' · ', c.gpu) AS metric, AVG(c.ratio) AS value FROM calibration_samples c WHERE $__timeFilter(c.`at`) AND c.server IN (${server:singlequote}) GROUP BY 1,2 ORDER BY 1",
    desc="How long each render or animation phase took compared with what was expected on the reference machine (RTX 5060 Ti).",
    defaults={"custom": {"drawStyle": "line", "lineWidth": 2}, "decimals": 2}))
panels.append(panel(10, "Slowest runs", "table", 12, 21, 12, 8,
    f"SELECT r.id AS run, r.project AS project, r.server AS server, TIMESTAMPDIFF(SECOND, r.started_at, r.finished_at) AS seconds FROM runs r WHERE $__timeFilter(r.created_at) AND r.state='succeeded' AND r.started_at IS NOT NULL AND {SERVER} ORDER BY seconds DESC LIMIT 15",
    "table", defaults={}, overrides=[{"matcher": {"id": "byName", "options": "seconds"}, "properties": [{"id": "unit", "value": "s"}, {"id": "decimals", "value": 0}]}]))
panels.append(panel(11, "Latest failures", "table", 0, 29, 24, 8,
    f"SELECT r.created_at AS created, r.id AS run, r.project AS project, r.server AS server, r.state AS state, LEFT(r.error, 200) AS error FROM runs r WHERE $__timeFilter(r.created_at) AND r.state IN ('failed','interrupted') AND {SERVER} ORDER BY r.created_at DESC LIMIT 20",
    "table"))

dash = {
    "__inputs": [{"name": "DS_MYSQL", "label": "Veritmo MySQL", "description": "A user with SELECT on runs, run_samples and calibration_samples", "type": "datasource", "pluginId": "mysql", "pluginName": "MySQL"}],
    "__requires": [{"type": "datasource", "id": "mysql", "name": "MySQL", "version": "1.0.0"}],
    "title": "Veritmo", "uid": "veritmo-history", "tags": ["veritmo"],
    "schemaVersion": 39, "version": 1, "editable": True, "timezone": "browser",
    "time": {"from": "now-30d", "to": "now"}, "refresh": "",
    "templating": {"list": [{
        "name": "server", "label": "Server", "type": "query", "datasource": DS,
        "query": "SELECT DISTINCT server FROM runs WHERE server <> '' ORDER BY server",
        "definition": "SELECT DISTINCT server FROM runs WHERE server <> '' ORDER BY server",
        "refresh": 2, "multi": True, "includeAll": True, "current": {"selected": True, "text": ["All"], "value": ["$__all"]},
    }]},
    "panels": panels,
}

# ---------------------------------------------------------------------------------------------------------------
# The machine: what /api/metrics exposes, read by Prometheus.

PDS = {"type": "prometheus", "uid": "${DS_PROMETHEUS}"}
SRV = 'server=~"$server"'


def pq(expr, legend="", ref="A", instant=False):
    t = {"datasource": PDS, "editorMode": "code", "expr": expr, "legendFormat": legend, "refId": ref, "range": not instant}
    if instant:
        t["instant"] = True
    return t


def pp(pid, title, kind, x, y, w, h, targets, desc="", defaults=None, options=None, overrides=None):
    return {"id": pid, "title": title, "type": kind, "description": desc, "gridPos": {"x": x, "y": y, "w": w, "h": h},
            "datasource": PDS, "targets": targets, "fieldConfig": {"defaults": defaults or {}, "overrides": overrides or []}, "options": options or {}}


# Each series carries the labels Prometheus adds (job, instance); `server` comes from veritmo_info, joined by instance.
def with_server(metric):
    return f'{metric} * on(instance) group_left(server) max by (instance, server) (veritmo_info{{server=~"$server"}})'


def machine():
    ps = []
    ps.append(pp(1, "Server", "stat", 0, 0, 4, 4, [pq('count(veritmo_info{server=~"$server"})', instant=True)],
        desc="Servers Prometheus sees right now."))
    ps.append(pp(2, "Runs in progress", "stat", 4, 0, 4, 4, [pq(with_server('sum by (instance) (veritmo_runs{state=~"running|queued"})'), instant=True)],
        defaults={"decimals": 0}))
    ps.append(pp(3, "Database", "stat", 8, 0, 4, 4, [pq(with_server('veritmo_database_up'), "{{server}}", instant=True)],
        defaults={"mappings": [{"type": "value", "options": {"0": {"text": "down", "color": "red"}, "1": {"text": "ok", "color": "green"}}}]}))
    ps.append(pp(4, "Music services running", "stat", 12, 0, 6, 4,
        [pq(with_server('veritmo_music_service_running'), "{{server}} · {{service}}", instant=True)],
        defaults={"mappings": [{"type": "value", "options": {"0": {"text": "stopped"}, "1": {"text": "running", "color": "green"}}}]}))
    ps.append(pp(5, "Available RAM", "stat", 18, 0, 6, 4, [pq(with_server('veritmo_memory_available_bytes'), "{{server}}", instant=True)],
        defaults={"unit": "bytes"}))
    ps.append(pp(6, "VRAM used per card", "timeseries", 0, 4, 12, 9,
        [pq(with_server('veritmo_gpu_memory_used_bytes'), "{{server}} · GPU {{gpu}} {{name}}")],
        defaults={"unit": "bytes", "custom": {"lineWidth": 1, "fillOpacity": 10}}))
    ps.append(pp(7, "Card utilization", "timeseries", 12, 4, 12, 9,
        [pq(with_server('veritmo_gpu_utilization_ratio'), "{{server}} · GPU {{gpu}}")],
        defaults={"unit": "percentunit", "min": 0, "max": 1}))
    ps.append(pp(8, "Card temperature", "timeseries", 0, 13, 12, 8,
        [pq(with_server('veritmo_gpu_temperature_celsius'), "{{server}} · GPU {{gpu}}")], defaults={"unit": "celsius"}))
    ps.append(pp(9, "Card power draw", "timeseries", 12, 13, 12, 8,
        [pq(with_server('veritmo_gpu_power_watts'), "{{server}} · GPU {{gpu}}")], defaults={"unit": "watt"}))
    ps.append(pp(10, "RAM in use", "timeseries", 0, 21, 12, 8,
        [pq(with_server('veritmo_memory_used_bytes'), "{{server}} used"), pq(with_server('veritmo_memory_total_bytes'), "{{server}} total", "B")],
        defaults={"unit": "bytes"}))
    ps.append(pp(11, "Free disk space", "timeseries", 12, 21, 12, 8,
        [pq(with_server('veritmo_disk_free_bytes'), "{{server}} · {{folder}}")], defaults={"unit": "bytes"}))
    ps.append(pp(12, "Runs per state", "timeseries", 0, 29, 12, 8,
        [pq(with_server('sum by (instance, state) (veritmo_runs)').replace("by (instance, state)", "by (instance, state)"), "{{server}} · {{state}}")],
        desc="The runs in each server's history, by state; deleting runs lowers them.", defaults={"decimals": 0}))
    ps.append(pp(13, "Duration of succeeded runs (p50 and p95)", "timeseries", 12, 29, 12, 8, [
        pq('histogram_quantile(0.5, sum by (le, instance) (veritmo_run_duration_seconds_bucket))', "{{instance}} p50"),
        pq('histogram_quantile(0.95, sum by (le, instance) (veritmo_run_duration_seconds_bucket))', "{{instance}} p95", "B")],
        desc="From the last 1000 succeeded runs of each server.", defaults={"unit": "s"}))
    return {
        "__inputs": [{"name": "DS_PROMETHEUS", "label": "Prometheus", "description": "The one that scrapes Veritmo's /api/metrics", "type": "datasource", "pluginId": "prometheus", "pluginName": "Prometheus"}],
        "__requires": [{"type": "datasource", "id": "prometheus", "name": "Prometheus", "version": "1.0.0"}],
        "title": "Veritmo · machine", "uid": "veritmo-machine", "tags": ["veritmo"],
        "schemaVersion": 39, "version": 1, "editable": True, "timezone": "browser",
        "time": {"from": "now-6h", "to": "now"}, "refresh": "30s",
        "templating": {"list": [{
            "name": "server", "label": "Server", "type": "query", "datasource": PDS,
            "query": {"query": "label_values(veritmo_info, server)", "refId": "S"},
            "definition": "label_values(veritmo_info, server)", "refresh": 2, "multi": True, "includeAll": True,
            "current": {"selected": True, "text": ["All"], "value": ["$__all"]}, "allValue": ".+",
        }]},
        "panels": ps,
    }


if len(sys.argv) > 1 and sys.argv[1] == "machine":
    print(json.dumps(machine(), indent=2, ensure_ascii=False))
else:
    print(json.dumps(dash, indent=2, ensure_ascii=False))
