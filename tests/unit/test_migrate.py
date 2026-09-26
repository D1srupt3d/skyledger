import pytest

from skyledger import migrate


def test_shipped_migrations_are_well_formed():
    found = migrate.discover()
    assert found[0] == (1, "0001_init.sql")
    assert [v for v, _ in found] == list(range(1, len(found) + 1)), "versions must be contiguous"
    for _, name in found:
        assert migrate.read(name).strip()


def test_order_is_numeric():
    assert migrate.discover(["0010_b.sql", "0002_a.sql"]) == [(2, "0002_a.sql"), (10, "0010_b.sql")]


@pytest.mark.parametrize("names, message", [
    (["1_init.sql"], "bad migration file name"),
    (["0001_Init.sql"], "bad migration file name"),
    (["0001_a.sql", "0001_b.sql"], "two migrations with version 1"),
])
def test_rejects(names, message):
    with pytest.raises(ValueError, match=message):
        migrate.discover(names)


def test_init_uses_current_columnstore_api():
    init = migrate.read("0001_init.sql")
    # add_compression_policy / timescaledb.compress are deprecated since TimescaleDB 2.18.
    assert "add_compression_policy" not in init and "timescaledb.compress " not in init
    assert init.count("add_columnstore_policy") == 3
