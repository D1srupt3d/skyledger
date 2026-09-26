import importlib.util
import json
import pathlib

ROOT = pathlib.Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("gen_dashboards", ROOT / "tools" / "gen_dashboards.py")
gen = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gen)


def dashboards():
    return {name: json.loads(text) for name, text in gen.render().items()}


def test_committed_files_are_current():
    assert gen.main(["--check"]) == 0


def test_uids_and_nav():
    d = dashboards()
    uids = sorted(x["uid"] for x in d.values())
    assert uids == ["skyledger-history", "skyledger-overview", "skyledger-receiver"]
    assert all(x["tags"] == ["skyledger"] and x["links"][0]["tags"] == ["skyledger"] for x in d.values())


def test_layout_has_no_overlaps():
    for name, dash in dashboards().items():
        cells = set()
        ids = [p["id"] for p in dash["panels"]]
        assert len(ids) == len(set(ids)), name
        for p in dash["panels"]:
            g = p["gridPos"]
            assert g["x"] + g["w"] <= 24, (name, p["title"])
            for x in range(g["x"], g["x"] + g["w"]):
                for y in range(g["y"], g["y"] + g["h"]):
                    assert (x, y) not in cells, (name, p["title"])
                    cells.add((x, y))


def test_every_query_uses_the_skyledger_datasource():
    for dash in dashboards().values():
        for p in dash["panels"]:
            for t in p.get("targets", []):
                assert t["datasource"] == gen.DS, p["title"]
                assert "rawSql" in t


def test_nothing_site_specific():
    text = "".join(gen.render().values())
    # (A pinned map centre is covered by test_maps_fit_to_data; "coords" also names the lat/lon layer mode.)
    for banned in ("America/", "Europe/", "kubectl", "prometheus", "adsb-history"):
        assert banned not in text, banned


def test_maps_fit_to_data():
    maps = [p for d in dashboards().values() for p in d["panels"] if p["type"] == "geomap"]
    assert len(maps) == 2
    for m in maps:
        assert m["options"]["view"]["id"] == "fit"
        assert any(layer["type"] == "markers" for layer in m["options"]["layers"]), m["title"]


def test_all_queries_covers_every_panel():
    queries = gen.all_queries()
    panels = [p for d in dashboards().values() for p in d["panels"] if p["type"] != "row"]
    assert len(queries) >= len(panels)
    assert all("history_daily" not in q for q in queries.values())
