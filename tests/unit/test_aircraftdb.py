import pytest

from skyledger import aircraftdb as db


@pytest.mark.parametrize("flags, bits", [
    ("00", 0), ("10", 1), ("01", 2), ("0010", 4), ("0001", 8), ("11", 3), ("1001", 9), (None, 0), ("", 0)])
def test_flag_bits(flags, bits):
    assert db.flag_bits(flags) == bits


def test_db_rows():
    rows = db.db_rows("AD", {"0001": ["N100XX", "B739", "00", "BOEING 737-900"],
                             "0003": ["N100XY", None, "0001", None], "children": ["AD0", "AD1"]})
    assert rows == [("ad0001", "N100XX", "B739", "BOEING 737-900", 0), ("ad0003", "N100XY", None, None, 8)]
    assert db.db_rows("A", {"E0000": ["00-0001", "T38", "10", "NORTHROP T-38"]}) == [
        ("ae0000", "00-0001", "T38", "NORTHROP T-38", 1)]


def test_airline_rows():
    assert db.airline_rows({"AAA": {"n": "Example Air", "c": "Nowhere", "r": "EXAMPLE"},
                            "XXX": {"n": "", "c": "", "r": ""}}) == [
        ("AAA", "Example Air", "Nowhere", "EXAMPLE"), ("XXX", None, None, None)]


def test_db_version():
    assert db.db_version('<script>databaseFolder = "db-3.14.1718";</script>') == "db-3.14.1718"
    with pytest.raises(ValueError):
        db.db_version("<html></html>")
