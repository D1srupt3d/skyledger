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


def test_maps_fit_all_layers():
    # Grafana 13 never fits a map to one named layer, so both maps fit all layers; the outline
    # rides along as a markers layer because heatmap and route layers give "fit" no extent.
    maps = {p["title"]: p for d in dashboards().values() for p in d["panels"] if p["type"] == "geomap"}
    fit_all = {"id": "fit", "allLayers": True, "padding": 5, "maxZoom": 9}
    for m in maps.values():
        assert m["options"]["view"] == fit_all, m["title"]
        assert any(layer["type"] == "markers" for layer in m["options"]["layers"]), m["title"]
    assert set(maps) == {"Reception range", "Where positions were received"}


def test_day_hour_grid_shows_one_week_of_dates():
    grid = next(p for p in dashboards()["skyledger-history.json"]["panels"]
                if p["title"] == "Flights per hour, last 7 days")
    assert grid["timeFrom"] == "7d"
    sql = grid["targets"][0]["rawSql"]
    assert "'Dy DD Mon'" in sql and "GROUP BY t::date ORDER BY t::date" in sql
    assert [f'"{h:02d}"' in sql for h in range(24)] == [True] * 24
    assert grid["fieldConfig"]["defaults"]["custom"]["width"] == 44


def test_dashboards_live_in_the_chart():
    assert gen.OUT == ROOT / "charts" / "skyledger" / "grafana" / "dashboards"


def test_grouped_queries_fill_empty_buckets_with_null():
    # Without a fill, an outage returns no rows and Grafana draws a straight line across it.
    grouped = {k: q for k, q in gen.SQL.items() if "$__timeGroupAlias" in q}
    assert len(grouped) == 6
    for name, sql in grouped.items():
        assert "$__timeGroupAlias(time, $__interval, NULL)" in sql, name
