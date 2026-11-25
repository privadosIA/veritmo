package workspace

// metrics.go: GET /api/metrics, the state of this server in the text format Prometheus reads. Everything is worked
// out when it is asked (nothing is kept between scrapes), only about this server (a history several servers share
// is counted by each one for its own runs) and never names a project, so a project hidden by the content mode is
// not told in a label. It needs the same token as the rest of /api. See specs/prometheus-metrics.

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"veritmo/internal/platform/edition"
	"veritmo/internal/platform/sched"
)

// durationBuckets are the upper bounds, in seconds, of the histogram of how long a run takes.
var durationBuckets = []float64{30, 60, 120, 300, 600, 1200, 1800, 3600, 7200, 14400}

// durationSamples is how many of the latest successful runs of this server the histogram counts.
const durationSamples = 1000

// metricsWriter writes the exposition format: one HELP and TYPE per family, then its samples.
type metricsWriter struct {
	b strings.Builder
}

type sample struct {
	labels [][2]string
	value  float64
}

func labelsOf(kv ...string) [][2]string {
	var l [][2]string
	for i := 0; i+1 < len(kv); i += 2 {
		l = append(l, [2]string{kv[i], kv[i+1]})
	}
	return l
}

func escapeLabel(v string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(v)
}

func formatLabels(l [][2]string) string {
	if len(l) == 0 {
		return ""
	}
	parts := make([]string, len(l))
	for i, kv := range l {
		parts[i] = kv[0] + `="` + escapeLabel(kv[1]) + `"`
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func formatValue(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

// family writes a metric family with its samples (a family with none is left out).
func (m *metricsWriter) family(name, typ, help string, samples ...sample) {
	if len(samples) == 0 {
		return
	}
	fmt.Fprintf(&m.b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
	for _, s := range samples {
		fmt.Fprintf(&m.b, "%s%s %s\n", name, formatLabels(s.labels), formatValue(s.value))
	}
}

func (m *metricsWriter) gauge(name, help string, value float64, kv ...string) {
	m.family(name, "gauge", help, sample{labelsOf(kv...), value})
}

// histogram writes, for each group of observed values, its buckets, its sum and its count; the labels of the
// group go on every line.
func (m *metricsWriter) histogram(name, help string, bounds []float64, groups map[runSeries][]float64) {
	fmt.Fprintf(&m.b, "# HELP %s %s\n# TYPE %s histogram\n", name, help, name)
	for _, k := range sortedSeries(groups) {
		values := groups[k]
		var sum float64
		for _, v := range values {
			sum += v
		}
		l := func(extra ...string) string {
			return formatLabels(labelsOf(append([]string{"client", k.Client, "kind", k.Kind}, extra...)...))
		}
		for _, le := range bounds {
			n := 0
			for _, v := range values {
				if v <= le {
					n++
				}
			}
			fmt.Fprintf(&m.b, "%s_bucket%s %d\n", name, l("le", formatValue(le)), n)
		}
		fmt.Fprintf(&m.b, "%s_bucket%s %d\n%s_sum%s %s\n%s_count%s %d\n", name, l("le", "+Inf"), len(values), name, l(), formatValue(sum), name, l(), len(values))
	}
}

// gpuMetric is one card as nvidia-smi reports it.
type gpuMetric struct {
	Index                int
	Name                 string
	UsedMB, TotalMB      float64
	UtilPct, TempC, Watt float64
}

// readGPUMetrics asks nvidia-smi for every card in one call; an unavailable counter ([N/A]) reads as 0.
func (h *Handler) readGPUMetrics(ctx context.Context) ([]gpuMetric, error) {
	if _, err := h.host.LookPath("nvidia-smi"); err != nil {
		return nil, err
	}
	out, err := h.host.EnvRunner().Run(ctx, "nvidia-smi", []string{
		"--query-gpu=index,name,memory.used,memory.total,utilization.gpu,temperature.gpu,power.draw", "--format=csv,noheader,nounits"})
	if err != nil {
		return nil, err
	}
	var gpus []gpuMetric
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, ",")
		if len(f) < 7 {
			continue
		}
		num := func(i int) float64 {
			v, err := strconv.ParseFloat(strings.TrimSpace(f[i]), 64)
			if err != nil {
				return 0
			}
			return v
		}
		idx, err := strconv.Atoi(strings.TrimSpace(f[0]))
		if err != nil {
			continue
		}
		gpus = append(gpus, gpuMetric{Index: idx, Name: strings.TrimSpace(f[1]), UsedMB: num(2), TotalMB: num(3), UtilPct: num(4), TempC: num(5), Watt: num(6)})
	}
	return gpus, nil
}

// GET /api/metrics -> text/plain in the Prometheus exposition format (version 0.0.4)
//
// Prometheus asks for it with the access token (`authorization: credentials:` in its scrape configuration, and
// `metrics_path: /api/metrics`).
func (h *Handler) handleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	var m metricsWriter
	server := h.cfg.ServerName
	driver := "sqlite"
	if h.cfg.Database.IsRemote() {
		driver = h.cfg.Database.Driver
	}
	m.gauge("veritmo_info", "This server, the engine its history is kept in and the version of the program.", 1, "server", server, "database", driver, "version", edition.VersionNumber())
	m.gauge("veritmo_uptime_seconds", "Seconds since the server started.", time.Since(h.host.Started()).Seconds())

	// The history.
	dbUp := 0.0
	if sqlDB, err := h.rm.hist.db.DB(); err == nil && sqlDB.PingContext(ctx) == nil {
		dbUp = 1
	}
	m.gauge("veritmo_database_up", "1 when the database of the history answers.", dbUp)
	if dbUp == 1 {
		h.runMetrics(&m, server)
	}

	// The machine.
	if used, total, err := readRAMProc(); err == nil {
		m.gauge("veritmo_memory_used_bytes", "RAM in use (total minus available).", float64(used)*1048576)
		m.gauge("veritmo_memory_total_bytes", "RAM of the machine.", float64(total)*1048576)
	}
	if avail, err := sched.MemAvailable(); err == nil {
		m.gauge("veritmo_memory_available_bytes", "RAM available (what can be used without swapping).", float64(avail))
	}
	var disks []sample
	for _, f := range []struct{ name, dir string }{{"projects", h.cfg.ProjectsDir}, {"models", filepath.Join(h.host.AssetsRoot(), "models")}} {
		if f.dir == "" {
			continue
		}
		if free, err := FreeDiskBytes(f.dir); err == nil {
			disks = append(disks, sample{labelsOf("folder", f.name), float64(free)})
		}
	}
	m.family("veritmo_disk_free_bytes", "gauge", "Free space of the disk that holds the folder.", disks...)
	if gpus, err := h.readGPUMetrics(ctx); err == nil && len(gpus) > 0 {
		var used, total, util, temp, watt []sample
		for _, g := range gpus {
			l := labelsOf("gpu", strconv.Itoa(g.Index), "name", g.Name)
			used = append(used, sample{l, g.UsedMB * 1048576})
			total = append(total, sample{l, g.TotalMB * 1048576})
			util = append(util, sample{l, g.UtilPct / 100})
			temp = append(temp, sample{l, g.TempC})
			watt = append(watt, sample{l, g.Watt})
		}
		m.family("veritmo_gpu_memory_used_bytes", "gauge", "Video memory in use.", used...)
		m.family("veritmo_gpu_memory_total_bytes", "gauge", "Video memory of the card.", total...)
		m.family("veritmo_gpu_utilization_ratio", "gauge", "How busy the card is, from 0 to 1.", util...)
		m.family("veritmo_gpu_temperature_celsius", "gauge", "Temperature of the card.", temp...)
		m.family("veritmo_gpu_power_watts", "gauge", "Power the card draws.", watt...)
	}

	// The music services (HeartMuLa, DiffRhythm2, LAION-CLAP).
	var running, starting []sample
	engines := h.host.Engines(ctx)
	names := make([]string, 0, len(engines))
	for n := range engines {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		a := engines[n]
		if a.Mode != "local" || a.Container == nil {
			continue
		}
		l := labelsOf("service", n)
		running = append(running, sample{l, boolFloat(a.Container.State == "running")})
		starting = append(starting, sample{l, boolFloat(a.Container.State == "starting")})
	}
	m.family("veritmo_music_service_running", "gauge", "1 when the service runs on this machine and is ready.", running...)
	m.family("veritmo_music_service_starting", "gauge", "1 while the service loads its model.", starting...)

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(m.b.String()))
}

func boolFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// maxMetricClients is how many different clients one reading of the metrics names; the rest are added up as "other".
const maxMetricClients = 20

// runKindSQL is RunKindOf as an expression of the query (the history can be any of the engines gorm talks to).
const runKindSQL = "CASE WHEN project LIKE 'api-gen-%' THEN 'video' WHEN project LIKE 'api-edit-%' THEN 'edit' WHEN project LIKE 'api-m3d-%' THEN 'models3d' ELSE 'project' END"

// runSeries is one series of the run metrics: what asked for the runs and the way they were asked for.
type runSeries struct{ Client, Kind string }

// runMetrics adds the runs of this server: how many are in each state and how long the successful ones took, by
// client and kind (see spec add-client-label). Only the clients with most runs are named, up to maxMetricClients.
func (h *Handler) runMetrics(m *metricsWriter, server string) {
	type count struct {
		State, Client, Kind string
		N                   int64
	}
	var counts []count
	q := h.rm.hist.db.Model(&Run{}).Select("state, client, " + runKindSQL + " AS kind, COUNT(*) AS n").Group("state, client, " + runKindSQL)
	if server != "" {
		q = q.Where("server = ? OR server IS NULL OR server = ''", server)
	}
	if err := q.Scan(&counts).Error; err != nil {
		counts = nil
	} else {
		perClient := map[string]int64{}
		for _, c := range counts {
			perClient[effectiveClient(c.Client, c.Kind)] += c.N
		}
		named := namedClients(perClient)
		have := map[runSeries]map[string]float64{}
		for _, c := range counts {
			k := runSeries{named(effectiveClient(c.Client, c.Kind)), c.Kind}
			if have[k] == nil {
				have[k] = map[string]float64{}
			}
			have[k][c.State] += float64(c.N)
		}
		if len(have) == 0 {
			have[runSeries{clientEmbedded, kindProjectRun}] = map[string]float64{}
		}
		var rows []sample
		for _, k := range sortedSeries(have) {
			for _, st := range []RunState{StateQueued, StateRunning, StateSucceeded, StateFailed, StateCancelled, StateInterrupted} {
				rows = append(rows, sample{labelsOf("state", string(st), "client", k.Client, "kind", k.Kind), have[k][string(st)]})
			}
		}
		m.family("veritmo_runs", "gauge", "Runs of this server in the history, by state, client and kind (deleting a run lowers it).", rows...)
		h.durationMetrics(m, server, named)
	}
}

// durationMetrics is the histogram of how long the latest successful runs took, one series per client and kind.
func (h *Handler) durationMetrics(m *metricsWriter, server string, named func(string) string) {
	var runs []Run
	dq := h.rm.hist.db.Select("started_at", "finished_at", "project", "client").Where("state = ? AND started_at IS NOT NULL AND finished_at IS NOT NULL", StateSucceeded).Order("created_at DESC").Limit(durationSamples)
	if server != "" {
		dq = dq.Where("server = ? OR server IS NULL OR server = ''", server)
	}
	if err := dq.Find(&runs).Error; err != nil {
		return
	}
	groups := map[runSeries][]float64{}
	for _, r := range runs {
		if d := r.FinishedAt.Sub(*r.StartedAt).Seconds(); d >= 0 {
			k := runSeries{named(clientOf(r)), RunKindOf(r.Project)}
			groups[k] = append(groups[k], d)
		}
	}
	if len(groups) == 0 {
		groups[runSeries{clientEmbedded, kindProjectRun}] = nil
	}
	m.histogram("veritmo_run_duration_seconds", "How long the latest successful runs of this server took, by client and kind.", durationBuckets, groups)
}

// namedClients answers, for a client, the name it goes by in the metrics: its own when it is one of the
// maxMetricClients with most runs (ties by name), "other" otherwise.
func namedClients(perClient map[string]int64) func(string) string {
	names := make([]string, 0, len(perClient))
	for n := range perClient {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if perClient[names[i]] != perClient[names[j]] {
			return perClient[names[i]] > perClient[names[j]]
		}
		return names[i] < names[j]
	})
	keep := map[string]bool{}
	for i, n := range names {
		if i < maxMetricClients {
			keep[n] = true
		}
	}
	return func(c string) string {
		if keep[c] {
			return c
		}
		return clientOther
	}
}

func sortedSeries[V any](m map[runSeries]V) []runSeries {
	keys := make([]runSeries, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Client != keys[j].Client {
			return keys[i].Client < keys[j].Client
		}
		return keys[i].Kind < keys[j].Kind
	})
	return keys
}
