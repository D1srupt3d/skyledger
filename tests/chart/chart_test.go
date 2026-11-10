// Render tests for charts/skyledger: `helm template` in each mode, then assertions on the YAML.
package chart_test

import (
	"bytes"
	"errors"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/D1srupt3d/skyledger/internal/version"
)

// Go runs tests from the package directory, so the repo root is two levels up.
const (
	root = "../.."
	rx   = "tar1090Url=http://rx.example/"
)

var (
	chart      = filepath.Join(root, "charts", "skyledger")
	externalDB = []string{"timescaledb.enabled=false", "database.host=db.example"}
)

type obj = map[string]any

type key struct{ kind, name string }

// helm runs the helm CLI, skipping the test when helm is not on PATH.
func helm(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not installed")
	}
	var out, errOut bytes.Buffer
	cmd := exec.Command("helm", args...)
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	return out.String(), errOut.String(), err
}

func run(t *testing.T, valuesFile string, sets []string) (string, string, error) {
	t.Helper()
	args := []string{"template", "skyledger", chart, "--namespace", "sky"}
	for _, s := range sets {
		args = append(args, "--set", s)
	}
	if valuesFile != "" {
		args = append(args, "-f", valuesFile)
	}
	return helm(t, args...)
}

func renderFile(t *testing.T, valuesFile string, sets ...string) []obj {
	t.Helper()
	stdout, stderr, err := run(t, valuesFile, sets)
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, stderr)
	}
	var docs []obj
	dec := yaml.NewDecoder(strings.NewReader(stdout))
	for {
		var d obj
		err := dec.Decode(&d)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decode helm output: %v", err)
		}
		if len(d) > 0 {
			docs = append(docs, d)
		}
	}
	return docs
}

func render(t *testing.T, sets ...string) []obj {
	t.Helper()
	return renderFile(t, "", sets...)
}

func renderFails(t *testing.T, sets ...string) string {
	t.Helper()
	_, stderr, err := run(t, "", sets)
	if err == nil {
		t.Fatalf("helm template with %v succeeded, want failure", sets)
	}
	return stderr
}

func objects(t *testing.T, docs []obj) map[key]obj {
	t.Helper()
	out := map[key]obj{}
	for _, d := range docs {
		out[key{getStr(t, d, "kind"), getStr(t, d, "metadata", "name")}] = d
	}
	return out
}

func containers(t *testing.T, o obj) []obj {
	t.Helper()
	spec := getMap(t, o, "spec", "template", "spec")
	var all []any
	if _, ok := spec["initContainers"]; ok {
		all = append(all, getList(t, spec, "initContainers")...)
	}
	all = append(all, getList(t, spec, "containers")...)
	out := make([]obj, len(all))
	for i, c := range all {
		out[i] = getMap(t, c)
	}
	return out
}

func env(t *testing.T, container obj) obj {
	t.Helper()
	out := obj{}
	if _, ok := container["env"]; !ok {
		return out
	}
	for _, e := range getList(t, container, "env") {
		out[getStr(t, e, "name")] = getMap(t, e)
	}
	return out
}

// get walks maps by string key and lists by int index (negative counts from the end).
func get(t *testing.T, v any, path ...any) any {
	t.Helper()
	for i, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := v.(obj)
			if !ok {
				t.Fatalf("at %v: %T is not a map", path[:i+1], v)
			}
			if v, ok = m[k]; !ok {
				t.Fatalf("at %v: missing key", path[:i+1])
			}
		case int:
			l, ok := v.([]any)
			if k < 0 {
				k += len(l)
			}
			if !ok || k < 0 || k >= len(l) {
				t.Fatalf("at %v: no such list index in %T", path[:i+1], v)
			}
			v = l[k]
		default:
			t.Fatalf("bad path element %v", p)
		}
	}
	return v
}

func getMap(t *testing.T, v any, path ...any) obj {
	t.Helper()
	m, ok := get(t, v, path...).(obj)
	if !ok {
		t.Fatalf("at %v: not a map", path)
	}
	return m
}

func getList(t *testing.T, v any, path ...any) []any {
	t.Helper()
	l, ok := get(t, v, path...).([]any)
	if !ok {
		t.Fatalf("at %v: not a list", path)
	}
	return l
}

func getStr(t *testing.T, v any, path ...any) string {
	t.Helper()
	s, ok := get(t, v, path...).(string)
	if !ok {
		t.Fatalf("at %v: not a string", path)
	}
	return s
}

func eq(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func ok(t *testing.T, cond bool, format string, args ...any) {
	t.Helper()
	if !cond {
		t.Errorf(format, args...)
	}
}

func containsDeep(list []any, want any) bool {
	return slices.ContainsFunc(list, func(v any) bool { return reflect.DeepEqual(v, want) })
}

func readYAML(t *testing.T, path string) obj {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out obj
	if err := yaml.Unmarshal(b, &out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return out
}

func sortedKeys(m obj) []string { return slices.Sorted(maps.Keys(m)) }

func with(sets ...[]string) []string { return slices.Concat(sets...) }

func TestChartVersionMatchesThePackage(t *testing.T) {
	c := readYAML(t, filepath.Join(chart, "Chart.yaml"))
	eq(t, c["version"], version.Version)
	eq(t, c["appVersion"], version.Version)
}

func TestChartImagesMatchCompose(t *testing.T) {
	// Renovate bumps compose.yml and the chart together; this keeps the chart's defaults from drifting behind it.
	values := readYAML(t, filepath.Join(chart, "values.yaml"))
	services := getMap(t, readYAML(t, filepath.Join(root, "compose.yml")), "services")
	for _, name := range []string{"timescaledb", "grafana"} {
		got, want := get(t, values, name, "image"), get(t, services, name, "image")
		ok(t, got == want, "%s: chart image %v, compose image %v", name, got, want)
	}
}

func TestSkyledgerDeploymentsAreHardenedProbedAndPinned(t *testing.T) {
	objs := objects(t, render(t, rx))
	for _, name := range []string{"skyledger-ingest", "skyledger-history"} {
		dep := objs[key{"Deployment", name}]
		eq(t, get(t, dep, "spec", "replicas"), 1)
		eq(t, get(t, dep, "spec", "strategy", "type"), "Recreate")
		pod := getMap(t, dep, "spec", "template", "spec")
		eq(t, pod["automountServiceAccountToken"], false)
		eq(t, get(t, pod, "securityContext", "runAsNonRoot"), true)
		eq(t, get(t, pod, "securityContext", "runAsUser"), 10001)
		var inits []any
		for _, c := range getList(t, pod, "initContainers") {
			inits = append(inits, get(t, c, "name"))
		}
		eq(t, inits, []any{"migrate"})
		for _, c := range containers(t, dep) {
			eq(t, c["image"], "ghcr.io/d1srupt3d/skyledger:"+version.Version)
			sc := getMap(t, c, "securityContext")
			eq(t, sc["allowPrivilegeEscalation"], false)
			eq(t, sc["readOnlyRootFilesystem"], true)
			eq(t, get(t, sc, "capabilities", "drop"), []any{"ALL"})
		}
		main := getMap(t, pod, "containers", 0)
		_, live := main["livenessProbe"]
		_, ready := main["readinessProbe"]
		ok(t, live && ready, "%s: main container needs liveness and readiness probes", name)
		ann := getMap(t, dep, "spec", "template", "metadata", "annotations")
		_, cfg := ann["checksum/config"]
		_, sec := ann["checksum/secret"]
		ok(t, cfg && sec, "%s: missing checksum annotations: %v", name, ann)
	}
	// The first import runs for hours; only history needs the long rollout deadline.
	history, ingest := objs[key{"Deployment", "skyledger-history"}], objs[key{"Deployment", "skyledger-ingest"}]
	eq(t, get(t, history, "spec", "progressDeadlineSeconds"), 86400)
	_, has := getMap(t, ingest, "spec")["progressDeadlineSeconds"]
	ok(t, !has, "ingest should not set progressDeadlineSeconds")
	hc := getMap(t, history, "spec", "template", "spec", "containers", 0)
	eq(t, get(t, hc, "startupProbe", "failureThreshold"), 1440)
	cmd := getStr(t, hc, "livenessProbe", "exec", "command", -1)
	ok(t, strings.Contains(cmd, "-mmin -180"), "history liveness command %q", cmd)
	ic := getMap(t, ingest, "spec", "template", "spec", "containers", 0)
	for _, probe := range []string{"livenessProbe", "readinessProbe"} {
		cmd := getStr(t, ic, probe, "exec", "command", -1)
		ok(t, strings.Contains(cmd, "-mmin -2)"), "ingest %s command %q", probe, cmd)
	}
}

func TestSelectorsIncludeTheRelease(t *testing.T) {
	dep := objects(t, render(t, rx))[key{"Deployment", "skyledger-ingest"}]
	eq(t, get(t, dep, "spec", "selector", "matchLabels"), obj{
		"app.kubernetes.io/name": "skyledger", "app.kubernetes.io/instance": "skyledger",
		"app.kubernetes.io/component": "ingest"})
}

func TestConfigmapPointsAtTheBundledDatabaseByDefault(t *testing.T) {
	cm := getMap(t, objects(t, render(t, rx))[key{"ConfigMap", "skyledger"}], "data")
	eq(t, cm["TAR1090_URL"], "http://rx.example/")
	eq(t, []any{cm["POSTGRES_HOST"], cm["POSTGRES_PORT"], cm["POSTGRES_USER"], cm["POSTGRES_DB"]},
		[]any{"skyledger-timescaledb", "5432", "skyledger", "skyledger"})
	eq(t, []any{cm["HISTORY_AT"], cm["HISTORY_START"], cm["TZ"]}, []any{"01:15", "auto", "UTC"})
}

func TestConfigmapPointsAtAnExternalDatabase(t *testing.T) {
	objs := objects(t, render(t, with([]string{rx}, externalDB,
		[]string{"database.port=6543", "database.name=sl", "database.user=owner"})...))
	cm := getMap(t, objs[key{"ConfigMap", "skyledger"}], "data")
	eq(t, []any{cm["POSTGRES_HOST"], cm["POSTGRES_PORT"], cm["POSTGRES_USER"], cm["POSTGRES_DB"]},
		[]any{"db.example", "6543", "owner", "sl"})
}

func TestGrafanaDbPasswordReachesMigrateOnlyWithTheBundledDatabase(t *testing.T) {
	migrateEnv := func(sets ...string) obj {
		dep := objects(t, render(t, with([]string{rx}, sets)...))[key{"Deployment", "skyledger-ingest"}]
		return env(t, getMap(t, dep, "spec", "template", "spec", "initContainers", 0))
	}
	_, has := migrateEnv()["GRAFANA_DB_PASSWORD"]
	ok(t, has, "bundled database: migrate should get GRAFANA_DB_PASSWORD")
	_, has = migrateEnv(externalDB...)["GRAFANA_DB_PASSWORD"]
	ok(t, !has, "external database: migrate should not get GRAFANA_DB_PASSWORD")
	objs := objects(t, render(t, rx))
	for _, name := range []string{"skyledger-ingest", "skyledger-history"} {
		main := getMap(t, objs[key{"Deployment", name}], "spec", "template", "spec", "containers", 0)
		_, has := env(t, main)["GRAFANA_DB_PASSWORD"]
		ok(t, !has, "%s main container should not get GRAFANA_DB_PASSWORD", name)
	}
}

func TestGeneratedSecretIsKeptAndComplete(t *testing.T) {
	objs := objects(t, render(t, rx))
	secret := objs[key{"Secret", "skyledger"}]
	eq(t, get(t, secret, "metadata", "annotations", "helm.sh/resource-policy"), "keep")
	eq(t, sortedKeys(getMap(t, secret, "data")),
		[]string{"GRAFANA_ADMIN_PASSWORD", "GRAFANA_DB_PASSWORD", "POSTGRES_PASSWORD"})
	// One set of random values per render, so the checksum hashes the Secret actually applied.
	checksum := func(name string) any {
		return get(t, objs[key{"Deployment", name}], "spec", "template", "metadata", "annotations", "checksum/secret")
	}
	eq(t, checksum("skyledger-ingest"), checksum("skyledger-history"))
}

func TestExtrasReachMigrateAndMainContainers(t *testing.T) {
	extras := []string{
		"extraEnv[0].name=X", "extraEnv[0].value=on",
		"extraVolumeMounts[0].name=v", "extraVolumeMounts[0].mountPath=/v",
		"extraVolumes[0].name=v", "extraVolumes[0].emptyDir.medium=Memory",
	}
	dep := objects(t, render(t, with([]string{rx}, extras)...))[key{"Deployment", "skyledger-ingest"}]
	for _, c := range containers(t, dep) {
		eq(t, get(t, env(t, c), "X", "value"), "on")
		ok(t, containsDeep(getList(t, c, "volumeMounts"), obj{"name": "v", "mountPath": "/v"}),
			"%v: missing extra volume mount", c["name"])
	}
	ok(t, containsDeep(getList(t, dep, "spec", "template", "spec", "volumes"),
		obj{"name": "v", "emptyDir": obj{"medium": "Memory"}}), "missing extra volume")
}

func TestEmptyResourcesAreOmitted(t *testing.T) {
	dep := objects(t, render(t, rx))[key{"Deployment", "skyledger-ingest"}]
	_, has := getMap(t, dep, "spec", "template", "spec", "containers", 0)["resources"]
	ok(t, !has, "resources should be omitted when empty")
	dep = objects(t, render(t, rx, "resources.ingest.limits.memory=256Mi"))[key{"Deployment", "skyledger-ingest"}]
	eq(t, get(t, dep, "spec", "template", "spec", "containers", 0, "resources"),
		obj{"limits": obj{"memory": "256Mi"}})
}

func TestExistingSecretReplacesTheGeneratedOne(t *testing.T) {
	objs := objects(t, render(t, rx, "existingSecret=mine"))
	_, has := objs[key{"Secret", "skyledger"}]
	ok(t, !has, "generated Secret should not render")
	dep := objs[key{"Deployment", "skyledger-ingest"}]
	main := getMap(t, dep, "spec", "template", "spec", "containers", 0)
	eq(t, get(t, env(t, main), "POSTGRES_PASSWORD", "valueFrom", "secretKeyRef", "name"), "mine")
	_, has = getMap(t, dep, "spec", "template", "metadata", "annotations")["checksum/secret"]
	ok(t, !has, "checksum/secret should not be set with an existing secret")
}

func TestGuardRails(t *testing.T) {
	stderr := renderFails(t)
	ok(t, strings.Contains(stderr, "set tar1090Url"), "stderr: %s", stderr)
	stderr = renderFails(t, rx, "timescaledb.enabled=false")
	ok(t, strings.Contains(stderr, "set database.host"), "stderr: %s", stderr)
}

func TestBundledTimescaledb(t *testing.T) {
	objs := objects(t, render(t, rx))
	sts := objs[key{"StatefulSet", "skyledger-timescaledb"}]
	pod := getMap(t, sts, "spec", "template", "spec")
	eq(t, get(t, pod, "securityContext", "fsGroup"), 1000)
	eq(t, get(t, pod, "securityContext", "fsGroupChangePolicy"), "OnRootMismatch")
	c := getMap(t, pod, "containers", 0)
	eq(t, c["image"], "timescale/timescaledb-ha:pg18.6-ts2.30.1")
	args := getList(t, c, "args")
	ok(t, slices.Contains(args, any("max_locks_per_transaction=128")) &&
		slices.Contains(args, any("timescaledb.telemetry_level=off")), "args: %v", args)
	eq(t, get(t, env(t, c), "POSTGRES_USER", "value"), "skyledger")
	eq(t, get(t, env(t, c), "POSTGRES_DB", "value"), "skyledger")
	eq(t, get(t, c, "startupProbe", "failureThreshold"), 60)
	for _, p := range []string{"startupProbe", "livenessProbe", "readinessProbe"} {
		eq(t, get(t, c, p, "timeoutSeconds"), 5)
	}
	ok(t, slices.Contains(getList(t, c, "readinessProbe", "exec", "command"), any("127.0.0.1")),
		"readiness probe should target 127.0.0.1")
	eq(t, get(t, c, "securityContext", "allowPrivilegeEscalation"), false)
	eq(t, get(t, c, "securityContext", "capabilities", "drop"), []any{"ALL"})
	eq(t, get(t, sts, "spec", "volumeClaimTemplates", 0, "spec", "resources", "requests", "storage"), "20Gi")
	svc := getMap(t, objs[key{"Service", "skyledger-timescaledb"}], "spec")
	eq(t, svc["type"], "ClusterIP")
	_, has := svc["externalTrafficPolicy"]
	ok(t, !has, "ClusterIP service should not set externalTrafficPolicy")
}

func TestTimescaledbLoadbalancerKeepsClientAddresses(t *testing.T) {
	svc := objects(t, render(t, rx, "timescaledb.service.type=LoadBalancer"))[key{"Service", "skyledger-timescaledb"}]
	eq(t, get(t, svc, "spec", "externalTrafficPolicy"), "Local")
}

func TestExternalDatabaseRendersNoTimescaledb(t *testing.T) {
	objs := objects(t, render(t, with([]string{rx}, externalDB)...))
	for _, k := range []key{{"StatefulSet", "skyledger-timescaledb"}, {"Service", "skyledger-timescaledb"}} {
		_, has := objs[k]
		ok(t, !has, "%v should not render with an external database", k)
	}
}

func datasource(t *testing.T, sets ...string) obj {
	t.Helper()
	cm := objects(t, render(t, with([]string{rx}, sets)...))[key{"ConfigMap", "skyledger-grafana-datasources"}]
	var file obj
	if err := yaml.Unmarshal([]byte(getStr(t, cm, "data", "skyledger.yml")), &file); err != nil {
		t.Fatal(err)
	}
	return getMap(t, file, "datasources", 0)
}

func TestBundledGrafana(t *testing.T) {
	objs := objects(t, render(t, rx))
	dep := objs[key{"Deployment", "skyledger-grafana"}]
	pod := getMap(t, dep, "spec", "template", "spec")
	eq(t, get(t, dep, "spec", "strategy", "type"), "Recreate")
	eq(t, get(t, pod, "securityContext", "runAsUser"), 472)
	eq(t, get(t, pod, "securityContext", "fsGroup"), 472)
	c := getMap(t, pod, "containers", 0)
	eq(t, c["image"], "grafana/grafana:13.2.2")
	eq(t, get(t, env(t, c), "GF_SECURITY_ADMIN_PASSWORD", "valueFrom", "secretKeyRef", "key"), "GRAFANA_ADMIN_PASSWORD")
	eq(t, get(t, env(t, c), "GRAFANA_DB_PASSWORD", "valueFrom", "secretKeyRef", "key"), "GRAFANA_DB_PASSWORD")
	eq(t, get(t, c, "readinessProbe", "httpGet", "path"), "/api/health")
	eq(t, get(t, c, "securityContext", "capabilities", "drop"), []any{"ALL"})
	mounts := map[string]bool{}
	for _, m := range getList(t, c, "volumeMounts") {
		mounts[getStr(t, m, "mountPath")] = true
	}
	for _, p := range []string{"/var/lib/grafana", "/etc/grafana/provisioning/datasources",
		"/etc/grafana/provisioning/dashboards", "/etc/grafana/provisioning/alerting"} {
		ok(t, mounts[p], "grafana should mount %s", p)
	}
	_, has := objs[key{"PersistentVolumeClaim", "skyledger-grafana"}]
	ok(t, has, "grafana PVC should render")
	_, has = objs[key{"Ingress", "skyledger-grafana"}]
	ok(t, !has, "grafana Ingress should not render by default")
	eq(t, sortedKeys(getMap(t, objs[key{"ConfigMap", "skyledger-grafana-dashboards"}], "data")),
		[]string{"provider.yml", "skyledger-history.json", "skyledger-overview.json", "skyledger-receiver.json"})
	eq(t, sortedKeys(getMap(t, objs[key{"ConfigMap", "skyledger-grafana-alerting"}], "data")),
		[]string{"skyledger.yml"})
}

func TestDatasourceFollowsTheDatabase(t *testing.T) {
	ds := datasource(t)
	eq(t, []any{ds["uid"], ds["user"], ds["url"]}, []any{"skyledger", "skyledger_grafana", "skyledger-timescaledb:5432"})
	eq(t, get(t, ds, "jsonData", "sslmode"), "disable")
	eq(t, get(t, ds, "jsonData", "database"), "skyledger")
	ds = datasource(t, externalDB...)
	eq(t, ds["url"], "db.example:5432")
	eq(t, get(t, ds, "jsonData", "sslmode"), "require")
	eq(t, get(t, datasource(t, "grafana.datasourceSslmode=verify-full"), "jsonData", "sslmode"), "verify-full")
}

func TestGrafanaOptions(t *testing.T) {
	objs := objects(t, render(t, rx, "grafana.persistence.enabled=false", "grafana.ingress.enabled=true",
		"grafana.ingress.host=sky.example", "grafana.ingress.tlsSecret=sky-tls"))
	_, has := objs[key{"PersistentVolumeClaim", "skyledger-grafana"}]
	ok(t, !has, "grafana PVC should not render without persistence")
	ing := objs[key{"Ingress", "skyledger-grafana"}]
	eq(t, get(t, ing, "spec", "rules", 0, "host"), "sky.example")
	eq(t, get(t, ing, "spec", "tls", 0, "secretName"), "sky-tls")
	vols := map[string]obj{}
	for _, v := range getList(t, objs[key{"Deployment", "skyledger-grafana"}], "spec", "template", "spec", "volumes") {
		vols[getStr(t, v, "name")] = getMap(t, v)
	}
	_, has = vols["storage"]["emptyDir"]
	ok(t, has, "grafana storage should be an emptyDir: %v", vols["storage"])
}

func TestNoGrafana(t *testing.T) {
	for k := range objects(t, render(t, rx, "grafana.enabled=false")) {
		s := k.kind + "/" + k.name
		ok(t, !strings.Contains(s, "grafana"), "%s should not render without grafana", s)
	}
}

func TestGrafanaRestartsWhenTheDatasourceChanges(t *testing.T) {
	checksum := func(sets ...string) any {
		dep := objects(t, render(t, with([]string{rx}, sets)...))[key{"Deployment", "skyledger-grafana"}]
		return get(t, dep, "spec", "template", "metadata", "annotations", "checksum/provisioning")
	}
	def := checksum()
	ok(t, checksum(externalDB...) != def, "external database should change checksum/provisioning")
	ok(t, checksum("grafana.datasourceSslmode=verify-full") != def, "sslmode should change checksum/provisioning")
}

func TestGrafanaIngressHost(t *testing.T) {
	stderr := renderFails(t, rx, "grafana.ingress.enabled=true")
	ok(t, strings.Contains(stderr, "grafana.ingress.host is required"), "stderr: %s", stderr)
	ing := objects(t, render(t, rx, "grafana.ingress.enabled=true", "grafana.ingress.host=sky.example"))
	_, has := getMap(t, ing[key{"Ingress", "skyledger-grafana"}], "spec")["tls"]
	ok(t, !has, "ingress without tlsSecret should not set tls")
}

func TestDemoFeed(t *testing.T) {
	objs := objects(t, render(t, "demo.enabled=true"))
	dep := objs[key{"Deployment", "skyledger-demo-feed"}]
	c := getMap(t, dep, "spec", "template", "spec", "containers", 0)
	eq(t, c["args"], []any{"demo-feed", "--port", "8080"})
	eq(t, get(t, c, "readinessProbe", "httpGet", "path"), "/data/receiver.json")
	eq(t, get(t, objs[key{"ConfigMap", "skyledger"}], "data", "TAR1090_URL"), "http://skyledger-demo-feed:8080/")
	svc := objs[key{"Service", "skyledger-demo-feed"}]
	eq(t, get(t, svc, "spec", "selector"), get(t, dep, "spec", "template", "metadata", "labels"))
	eq(t, get(t, svc, "spec", "ports", 0, "port"), 8080)
}

func TestHomelabShapeNeedsOnlyTheDatabasePassword(t *testing.T) {
	objs := objects(t, renderFile(t, filepath.Join(root, "tests", "chart", "values-homelab.yaml")))
	for k := range objs {
		ok(t, k.kind != "StatefulSet" && k.kind != "Secret" && !strings.Contains(k.name, "grafana"),
			"%v should not render in the homelab shape", k)
	}
	refs := map[[2]string]bool{}
	for _, name := range []string{"skyledger-ingest", "skyledger-history"} {
		dep := objs[key{"Deployment", name}]
		for _, c := range containers(t, dep) {
			for _, e := range env(t, c) {
				if _, has := getMap(t, e)["valueFrom"]; has {
					ref := getMap(t, e, "valueFrom", "secretKeyRef")
					refs[[2]string{getStr(t, ref, "name"), getStr(t, ref, "key")}] = true
				}
			}
			eq(t, get(t, env(t, c), "SSL_CERT_FILE", "value"), "/etc/homelab-ca/ca.crt")
			var mounts []any
			for _, m := range getList(t, c, "volumeMounts") {
				mounts = append(mounts, get(t, m, "mountPath"))
			}
			ok(t, slices.Contains(mounts, any("/etc/homelab-ca")), "%v: missing /etc/homelab-ca mount", c["name"])
		}
		var vols []any
		for _, v := range getList(t, dep, "spec", "template", "spec", "volumes") {
			vols = append(vols, get(t, v, "name"))
		}
		ok(t, slices.Contains(vols, any("homelab-root-ca")), "%s: missing homelab-root-ca volume", name)
	}
	eq(t, refs, map[[2]string]bool{{"skyledger-db", "POSTGRES_PASSWORD"}: true})
	eq(t, get(t, objs[key{"ConfigMap", "skyledger"}], "data", "POSTGRES_HOST"), "timescaledb.timescaledb.svc")
}

func TestNotesRender(t *testing.T) {
	install := func(sets ...string) string {
		args := []string{"install", "skyledger", chart, "--dry-run=client"}
		for _, s := range sets {
			args = append(args, "--set", s)
		}
		stdout, stderr, err := helm(t, args...)
		if err != nil {
			t.Fatalf("helm install --dry-run: %v\n%s", err, stderr)
		}
		return stdout
	}
	out := install(rx)
	ok(t, strings.Contains(out, "port-forward svc/skyledger-grafana") && strings.Contains(out, "existingSecret"),
		"default notes:\n%s", out)
	_, notes, found := strings.Cut(install(rx, "grafana.enabled=false", "existingSecret=x"), "NOTES:")
	if !found {
		t.Fatal("no NOTES: section in helm install output")
	}
	ok(t, !strings.Contains(notes, "port-forward") && !strings.Contains(notes, "delete secret"), "notes:\n%s", notes)
	ok(t, strings.Contains(notes, "delete pvc data-skyledger-timescaledb-0"), "notes:\n%s", notes)
}
