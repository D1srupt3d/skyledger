import pathlib
import tomllib

import yaml

ROOT = pathlib.Path(__file__).resolve().parents[2]


def deployments():
    files = sorted((ROOT / "deploy/kubernetes").glob("*.yml"))
    docs = [d for f in files for d in yaml.safe_load_all(f.read_text()) if d]
    return [d for d in docs if d["kind"] == "Deployment"]


def containers(dep):
    spec = dep["spec"]["template"]["spec"]
    return spec.get("initContainers", []) + spec["containers"]


def test_images_match_the_package_version():
    version = tomllib.loads((ROOT / "pyproject.toml").read_text())["project"]["version"]
    images = {c["image"] for d in deployments() for c in containers(d)}
    assert images == {f"ghcr.io/d1srupt3d/skyledger:{version}"}


def test_both_services_are_hardened_and_probed():
    deps = {d["metadata"]["name"]: d for d in deployments()}
    assert set(deps) == {"skyledger-ingest", "skyledger-history"}
    for dep in deps.values():
        assert dep["spec"]["replicas"] == 1
        assert dep["spec"]["strategy"]["type"] == "Recreate"
        for c in containers(dep):
            sc = c["securityContext"]
            assert sc["allowPrivilegeEscalation"] is False and sc["readOnlyRootFilesystem"] is True
            assert sc["capabilities"]["drop"] == ["ALL"]
        main = dep["spec"]["template"]["spec"]["containers"][0]
        assert "livenessProbe" in main and "readinessProbe" in main
