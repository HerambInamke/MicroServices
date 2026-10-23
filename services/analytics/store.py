"""ClickHouse events_log: append-only, deduplicated on (event_id, line) because Kafka delivers at least once."""

from decimal import Decimal

import clickhouse_connect

COLUMNS = ["event_id", "line", "event_type", "ts", "order_id", "product_id", "wholesaler_id", "category_id", "status", "qty", "amount", "metadata"]

SCHEMA = """
CREATE TABLE IF NOT EXISTS {table} (
    event_id String,
    line UInt16,
    event_type LowCardinality(String),
    ts DateTime64(3, 'UTC'),
    order_id String,
    product_id UInt64,
    wholesaler_id String,
    category_id UInt64,
    status LowCardinality(String),
    qty UInt32,
    amount Decimal(18, 2),
    metadata String
) ENGINE = ReplacingMergeTree ORDER BY (event_id, line)
"""


class ClickHouseStore:
    def __init__(self, client, table: str = "events_log"):
        self.client, self.table = client, table

    @classmethod
    def connect(cls, url: str, table: str = "events_log") -> "ClickHouseStore":
        # No session id, so concurrent API requests can share the client.
        return cls(clickhouse_connect.get_client(dsn=url, autogenerate_session_id=False), table)

    def ensure(self) -> None:
        self.client.command(SCHEMA.format(table=self.table))

    def insert(self, rows: list[dict]) -> None:
        if rows:
            self.client.insert(self.table, [[r[c] for c in COLUMNS] for r in rows], column_names=COLUMNS)

    def _query(self, sql: str, params: dict) -> list[tuple]:
        # str.replace, not format: ClickHouse's {name:Type} parameters use braces too.
        return self.client.query(sql.replace("{t}", self.table), parameters=params).result_rows

    def seller_summary(self, wholesaler_id: str, days: int) -> dict:
        p = {"w": wholesaler_id, "days": days}
        since = "ts >= now64(3) - toIntervalDay({days:UInt32}) AND wholesaler_id = {w:String}"
        revenue, paid = self._query(
            f"SELECT sum(amount), uniqExact(order_id) FROM {{t}} FINAL WHERE event_type = 'PaymentCompleted' AND {since}", p
        )[0]
        # An order's current status is its latest status event; orders are counted when placed in the period.
        by_status = self._query(
            f"""SELECT status, count() FROM (
                    SELECT order_id, argMax(status, ts) AS status FROM {{t}} FINAL
                    WHERE event_type IN ('OrderPlaced', 'OrderStatusChanged', 'PaymentCompleted') AND wholesaler_id = {{w:String}}
                    GROUP BY order_id
                    HAVING minIf(ts, event_type = 'OrderPlaced') >= now64(3) - toIntervalDay({{days:UInt32}})
                ) GROUP BY status ORDER BY status""",
            p,
        )
        top = self._query(
            f"SELECT product_id, sum(qty) AS q FROM {{t}} FINAL WHERE event_type = 'OrderPlaced' AND {since} "
            "GROUP BY product_id ORDER BY q DESC, product_id LIMIT 5",
            p,
        )
        return {
            "revenue": float(revenue or Decimal("0")),
            "paidOrders": paid,
            "ordersByStatus": {s: n for s, n in by_status},
            "topProducts": [{"productId": pid, "qty": q} for pid, q in top],
        }

    def trends(self, category_id: int | None, days: int) -> list[dict]:
        where = "event_type = 'OrderPlaced' AND ts >= now64(3) - toIntervalDay({days:UInt32})"
        if category_id is not None:
            where += " AND category_id = {c:UInt64}"
        res = self._query(
            f"SELECT toStartOfWeek(ts, 1) AS week, category_id, uniqExact(order_id), sum(qty) FROM {{t}} FINAL WHERE {where} "
            "GROUP BY week, category_id ORDER BY week, category_id",
            {"days": days, "c": category_id},
        )
        return [{"week": w.isoformat(), "categoryId": c, "orders": o, "qty": q} for w, c, o, q in res]
