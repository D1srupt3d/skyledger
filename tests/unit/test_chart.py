"""Render tests for charts/skyledger: `helm template` in each mode, then assertions on the YAML."""

import pathlib
import shutil
import subprocess
import tomllib

import pytest
import yaml

ROOT = pathlib.Path(__file__).resolve().parents[2]
CHART = ROOT / "charts" / "skyledger"
VERSION = tomllib.loads((ROOT / "pyproject.toml").read_text())["project"]["version"]
RX = "tar1090Url=http://rx.example/"
EXTERNAL_DB = ("timescaledb.enabled=false", "database.host=db.example")

pytestmark = pytest.mark.skipif(shutil.which("helm") is None, reason="helm not installed")


def _run(sets, values_file=None):
    cmd = ["helm", "template", "skyledger", str(CHART), "--namespace", "sky"]
    for s in sets:
        cmd += ["--set", s]
    if values_file:
        cmd += ["-f", str(values_file)]
    return subprocess.run(cmd, capture_output=True, text=True)


def render(*sets, values_file=None):
    r = _run(sets, values_file)
    assert r.returncode == 0, r.stderr
    return [d for d in yaml.safe_load_all(r.stdout) if d]


def render_fails(*sets):
    r = _run(sets)
    assert r.returncode != 0
    return r.stderr


def objects(docs):
    return {(d["kind"], d["metadata"]["name"]): d for d in docs}


def containers(obj):
    spec = obj["spec"]["template"]["spec"]
    return spec.get("initContainers", []) + spec["containers"]


def env(container):
    return {e["name"]: e for e in container.get("env", [])}


def test_chart_version_matches_the_package():
    chart = yaml.safe_load((CHART / "Chart.yaml").read_text())
    assert chart["version"] == chart["appVersion"] == VERSION


def test_chart_images_match_compose():
    # Dependabot bumps compose.yml; this keeps the chart's defaults from drifting behind it.
    values = yaml.safe_load((CHART / "values.yaml").read_text())
    services = yaml.safe_load((ROOT / "compose.yml").read_text())["services"]
    for name in ("timescaledb", "grafana"):
        assert values[name]["image"] == services[name]["image"], name


def test_skyledger_deployments_are_hardened_probed_and_pinned():
    objs = objects(render(RX))
    for name in ("skyledger-ingest", "skyledger-history"):
        dep = objs[("Deployment", name)]
        assert dep["spec"]["replicas"] == 1 and dep["spec"]["strategy"]["type"] == "Recreate"
        pod = dep["spec"]["template"]["spec"]
        assert pod["automountServiceAccountToken"] is False
        assert pod["securityContext"]["runAsNonRoot"] is True and pod["securityContext"]["runAsUser"] == 10001
        assert [c["name"] for c in pod["initContainers"]] == ["migrate"]
        for c in containers(dep):
            assert c["image"] == f"ghcr.io/d1srupt3d/skyledger:{VERSION}"
            sc = c["securityContext"]
            assert sc["allowPrivilegeEscalation"] is False and sc["readOnlyRootFilesystem"] is True
            assert sc["capabilities"]["drop"] == ["ALL"]
        main = pod["containers"][0]
        assert "livenessProbe" in main and "readinessProbe" in main
        ann = dep["spec"]["template"]["metadata"]["annotations"]
        assert "checksum/config" in ann and "checksum/secret" in ann
    # The first import runs for hours; only history needs the long rollout deadline.
    assert objs[("Deployment", "skyledger-history")]["spec"]["progressDeadlineSeconds"] == 86400
    assert "progressDeadlineSeconds" not in objs[("Deployment", "skyledger-ingest")]["spec"]
    history = objs[("Deployment", "skyledger-history")]["spec"]["template"]["spec"]["containers"][0]
    assert history["startupProbe"]["failureThreshold"] == 1440
    assert "-mmin -180" in history["livenessProbe"]["exec"]["command"][-1]
    ingest = objs[("Deployment", "skyledger-ingest")]["spec"]["template"]["spec"]["containers"][0]
    for probe in ("livenessProbe", "readinessProbe"):
        assert "-mmin -2)" in ingest[probe]["exec"]["command"][-1]


def test_selectors_include_the_release():
    dep = objects(render(RX))[("Deployment", "skyledger-ingest")]
    assert dep["spec"]["selector"]["matchLabels"] == {
        "app.kubernetes.io/name": "skyledger", "app.kubernetes.io/instance": "skyledger",
        "app.kubernetes.io/component": "ingest"}


def test_configmap_points_at_the_bundled_database_by_default():
    cm = objects(render(RX))[("ConfigMap", "skyledger")]["data"]
    assert cm["TAR1090_URL"] == "http://rx.example/"
    assert (cm["POSTGRES_HOST"], cm["POSTGRES_PORT"], cm["POSTGRES_USER"], cm["POSTGRES_DB"]) == (
        "skyledger-timescaledb", "5432", "skyledger", "skyledger")
    assert cm["HISTORY_AT"] == "01:15" and cm["HISTORY_START"] == "auto" and cm["TZ"] == "UTC"


def test_configmap_points_at_an_external_database():
    cm = objects(render(RX, *EXTERNAL_DB, "database.port=6543", "database.name=sl", "database.user=owner"))
    cm = cm[("ConfigMap", "skyledger")]["data"]
    assert (cm["POSTGRES_HOST"], cm["POSTGRES_PORT"], cm["POSTGRES_USER"], cm["POSTGRES_DB"]) == (
        "db.example", "6543", "owner", "sl")


def test_grafana_db_password_reaches_migrate_only_with_the_bundled_database():
    def migrate_env(*sets):
        dep = objects(render(RX, *sets))[("Deployment", "skyledger-ingest")]
        return env(dep["spec"]["template"]["spec"]["initContainers"][0])
    assert "GRAFANA_DB_PASSWORD" in migrate_env()
    assert "GRAFANA_DB_PASSWORD" not in migrate_env(*EXTERNAL_DB)
    for name in ("skyledger-ingest", "skyledger-history"):
        main = objects(render(RX))[("Deployment", name)]["spec"]["template"]["spec"]["containers"][0]
        assert "GRAFANA_DB_PASSWORD" not in env(main)


def test_generated_secret_is_kept_and_complete():
    objs = objects(render(RX))
    secret = objs[("Secret", "skyledger")]
    assert secret["metadata"]["annotations"]["helm.sh/resource-policy"] == "keep"
    assert set(secret["data"]) == {"POSTGRES_PASSWORD", "GRAFANA_DB_PASSWORD", "GRAFANA_ADMIN_PASSWORD"}
    # One set of random values per render, so the checksum hashes the Secret actually applied.
    checksums = {
        objs[("Deployment", name)]["spec"]["template"]["metadata"]["annotations"]["checksum/secret"]
        for name in ("skyledger-ingest", "skyledger-history")
    }
    assert len(checksums) == 1


def test_extras_reach_migrate_and_main_containers():
    extras = (
        "extraEnv[0].name=X", "extraEnv[0].value=on",
        "extraVolumeMounts[0].name=v", "extraVolumeMounts[0].mountPath=/v",
        "extraVolumes[0].name=v", "extraVolumes[0].emptyDir.medium=Memory",
    )
    dep = objects(render(RX, *extras))[("Deployment", "skyledger-ingest")]
    for c in containers(dep):
        assert env(c)["X"]["value"] == "on"
        assert {"name": "v", "mountPath": "/v"} in c["volumeMounts"]
    assert {"name": "v", "emptyDir": {"medium": "Memory"}} in dep["spec"]["template"]["spec"]["volumes"]


def test_empty_resources_are_omitted():
    dep = objects(render(RX))[("Deployment", "skyledger-ingest")]
    assert "resources" not in dep["spec"]["template"]["spec"]["containers"][0]
    dep = objects(render(RX, "resources.ingest.limits.memory=256Mi"))[("Deployment", "skyledger-ingest")]
    assert dep["spec"]["template"]["spec"]["containers"][0]["resources"] == {"limits": {"memory": "256Mi"}}


def test_existing_secret_replaces_the_generated_one():
    objs = objects(render(RX, "existingSecret=mine"))
    assert ("Secret", "skyledger") not in objs
    dep = objs[("Deployment", "skyledger-ingest")]
    ref = env(dep["spec"]["template"]["spec"]["containers"][0])["POSTGRES_PASSWORD"]["valueFrom"]
    assert ref["secretKeyRef"]["name"] == "mine"
    assert "checksum/secret" not in dep["spec"]["template"]["metadata"]["annotations"]


def test_guard_rails():
    assert "set tar1090Url" in render_fails()
    assert "set database.host" in render_fails(RX, "timescaledb.enabled=false")


def test_bundled_timescaledb():
    objs = objects(render(RX))
    sts = objs[("StatefulSet", "skyledger-timescaledb")]
    pod = sts["spec"]["template"]["spec"]
    assert pod["securityContext"]["fsGroup"] == 1000
    assert pod["securityContext"]["fsGroupChangePolicy"] == "OnRootMismatch"
    c = pod["containers"][0]
    assert c["image"] == "timescale/timescaledb-ha:pg18.6-ts2.30.1"
    assert "max_locks_per_transaction=128" in c["args"] and "timescaledb.telemetry_level=off" in c["args"]
    assert env(c)["POSTGRES_USER"]["value"] == "skyledger" and env(c)["POSTGRES_DB"]["value"] == "skyledger"
    assert c["startupProbe"]["failureThreshold"] == 60
    assert all(c[p]["timeoutSeconds"] == 5 for p in ("startupProbe", "livenessProbe", "readinessProbe"))
    assert "127.0.0.1" in c["readinessProbe"]["exec"]["command"]
    assert c["securityContext"]["allowPrivilegeEscalation"] is False
    assert c["securityContext"]["capabilities"]["drop"] == ["ALL"]
    claim = sts["spec"]["volumeClaimTemplates"][0]
    assert claim["spec"]["resources"]["requests"]["storage"] == "20Gi"
    svc = objs[("Service", "skyledger-timescaledb")]
    assert svc["spec"]["type"] == "ClusterIP" and "externalTrafficPolicy" not in svc["spec"]


def test_timescaledb_loadbalancer_keeps_client_addresses():
    svc = objects(render(RX, "timescaledb.service.type=LoadBalancer"))[("Service", "skyledger-timescaledb")]
    assert svc["spec"]["externalTrafficPolicy"] == "Local"


def test_external_database_renders_no_timescaledb():
    kinds = {k for k in objects(render(RX, *EXTERNAL_DB))}
    assert ("StatefulSet", "skyledger-timescaledb") not in kinds
    assert ("Service", "skyledger-timescaledb") not in kinds


def _datasource(*sets):
    cm = objects(render(RX, *sets))[("ConfigMap", "skyledger-grafana-datasources")]
    return yaml.safe_load(cm["data"]["skyledger.yml"])["datasources"][0]


def test_bundled_grafana():
    objs = objects(render(RX))
    dep = objs[("Deployment", "skyledger-grafana")]
    pod = dep["spec"]["template"]["spec"]
    assert dep["spec"]["strategy"]["type"] == "Recreate"
    assert pod["securityContext"]["runAsUser"] == 472 and pod["securityContext"]["fsGroup"] == 472
    c = pod["containers"][0]
    assert c["image"] == "grafana/grafana:13.2.2"
    admin = env(c)["GF_SECURITY_ADMIN_PASSWORD"]["valueFrom"]["secretKeyRef"]
    assert admin["key"] == "GRAFANA_ADMIN_PASSWORD"
    assert env(c)["GRAFANA_DB_PASSWORD"]["valueFrom"]["secretKeyRef"]["key"] == "GRAFANA_DB_PASSWORD"
    assert c["readinessProbe"]["httpGet"]["path"] == "/api/health"
    assert c["securityContext"]["capabilities"]["drop"] == ["ALL"]
    mounts = {m["mountPath"] for m in c["volumeMounts"]}
    assert {"/var/lib/grafana", "/etc/grafana/provisioning/datasources",
            "/etc/grafana/provisioning/dashboards", "/etc/grafana/provisioning/alerting"} <= mounts
    assert ("PersistentVolumeClaim", "skyledger-grafana") in objs
    assert ("Ingress", "skyledger-grafana") not in objs
    dashboards = objs[("ConfigMap", "skyledger-grafana-dashboards")]["data"]
    assert set(dashboards) == {"provider.yml", "skyledger-overview.json", "skyledger-receiver.json",
                               "skyledger-history.json"}
    assert set(objs[("ConfigMap", "skyledger-grafana-alerting")]["data"]) == {"skyledger.yml"}


def test_datasource_follows_the_database():
    ds = _datasource()
    assert (ds["uid"], ds["user"], ds["url"]) == (
        "skyledger", "skyledger_grafana", "skyledger-timescaledb:5432")
    assert ds["jsonData"]["sslmode"] == "disable" and ds["jsonData"]["database"] == "skyledger"
    ds = _datasource(*EXTERNAL_DB)
    assert ds["url"] == "db.example:5432" and ds["jsonData"]["sslmode"] == "require"
    assert _datasource("grafana.datasourceSslmode=verify-full")["jsonData"]["sslmode"] == "verify-full"


def test_grafana_options():
    objs = objects(render(RX, "grafana.persistence.enabled=false", "grafana.ingress.enabled=true",
                          "grafana.ingress.host=sky.example", "grafana.ingress.tlsSecret=sky-tls"))
    assert ("PersistentVolumeClaim", "skyledger-grafana") not in objs
    ing = objs[("Ingress", "skyledger-grafana")]
    assert ing["spec"]["rules"][0]["host"] == "sky.example"
    assert ing["spec"]["tls"][0]["secretName"] == "sky-tls"
    pod = objs[("Deployment", "skyledger-grafana")]["spec"]["template"]["spec"]
    vols = {v["name"]: v for v in pod["volumes"]}
    assert "emptyDir" in vols["storage"]


def test_no_grafana():
    kinds = {k[0] + "/" + k[1] for k in objects(render(RX, "grafana.enabled=false"))}
    assert not [k for k in kinds if "grafana" in k]


def test_grafana_restarts_when_the_datasource_changes():
    def checksum(*sets):
        dep = objects(render(RX, *sets))[("Deployment", "skyledger-grafana")]
        return dep["spec"]["template"]["metadata"]["annotations"]["checksum/provisioning"]
    default = checksum()
    assert checksum(*EXTERNAL_DB) != default
    assert checksum("grafana.datasourceSslmode=verify-full") != default


def test_grafana_ingress_host():
    assert "grafana.ingress.host is required" in render_fails(RX, "grafana.ingress.enabled=true")
    ing = objects(render(RX, "grafana.ingress.enabled=true", "grafana.ingress.host=sky.example"))
    assert "tls" not in ing[("Ingress", "skyledger-grafana")]["spec"]


def test_demo_feed():
    objs = objects(render("demo.enabled=true"))
    dep = objs[("Deployment", "skyledger-demo-feed")]
    c = dep["spec"]["template"]["spec"]["containers"][0]
    assert c["args"] == ["demo-feed", "--port", "8080"]
    assert c["readinessProbe"]["httpGet"]["path"] == "/data/receiver.json"
    assert objs[("ConfigMap", "skyledger")]["data"]["TAR1090_URL"] == "http://skyledger-demo-feed:8080/"
    svc = objs[("Service", "skyledger-demo-feed")]
    assert svc["spec"]["selector"] == dep["spec"]["template"]["metadata"]["labels"]
    assert svc["spec"]["ports"][0]["port"] == 8080


def test_homelab_shape_needs_only_the_database_password():
    objs = objects(render(values_file=ROOT / "tests" / "unit" / "values-homelab.yaml"))
    assert not [k for k in objs if k[0] in ("StatefulSet", "Secret") or "grafana" in k[1]]
    refs = set()
    for name in ("skyledger-ingest", "skyledger-history"):
        dep = objs[("Deployment", name)]
        for c in containers(dep):
            for e in c.get("env", []):
                if "valueFrom" in e:
                    ref = e["valueFrom"]["secretKeyRef"]
                    refs.add((ref["name"], ref["key"]))
            assert env(c)["SSL_CERT_FILE"]["value"] == "/etc/homelab-ca/ca.crt"
            assert "/etc/homelab-ca" in {m["mountPath"] for m in c["volumeMounts"]}
        vols = {v["name"] for v in dep["spec"]["template"]["spec"]["volumes"]}
        assert "homelab-root-ca" in vols
    assert refs == {("skyledger-db", "POSTGRES_PASSWORD")}
    assert objs[("ConfigMap", "skyledger")]["data"]["POSTGRES_HOST"] == "timescaledb.timescaledb.svc"


def test_notes_render():
    r = subprocess.run(["helm", "install", "skyledger", str(CHART), "--dry-run=client", "--set", RX],
                       capture_output=True, text=True)
    assert r.returncode == 0, r.stderr
    assert "port-forward svc/skyledger-grafana" in r.stdout and "existingSecret" in r.stdout
    r = subprocess.run(["helm", "install", "skyledger", str(CHART), "--dry-run=client", "--set", RX,
                        "--set", "grafana.enabled=false", "--set", "existingSecret=x"],
                       capture_output=True, text=True)
    assert r.returncode == 0, r.stderr
    notes = r.stdout.split("NOTES:", 1)[1]
    assert "port-forward" not in notes and "delete secret" not in notes
    assert "delete pvc data-skyledger-timescaledb-0" in notes
