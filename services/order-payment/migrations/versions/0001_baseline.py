"""Baseline: the schema create_all built before migrations.

Revision ID: 0001
Revises:
"""

import sqlalchemy as sa
from alembic import op

revision = "0001"
down_revision = None
branch_labels = None
depends_on = None


def upgrade() -> None:
    # Databases created by create_all before migrations already have these tables; adopt them as they are.
    existing = set(sa.inspect(op.get_bind()).get_table_names())

    if "orders" not in existing:
        op.create_table(
            "orders",
            sa.Column("id", sa.String(36), primary_key=True),
            sa.Column("retailer_id", sa.String(64), nullable=False),
            sa.Column("wholesaler_id", sa.String(64), nullable=False),
            sa.Column("status", sa.String(20), nullable=False),
            sa.Column("total", sa.Numeric(14, 2), nullable=False),
            sa.Column("currency", sa.String(3), nullable=False),
            sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
            sa.Column("updated_at", sa.DateTime(timezone=True), nullable=False),
        )
        op.create_index("ix_orders_retailer_id", "orders", ["retailer_id"])
        op.create_index("ix_orders_wholesaler_id", "orders", ["wholesaler_id"])
        op.create_index("ix_orders_status", "orders", ["status"])

    if "order_items" not in existing:
        op.create_table(
            "order_items",
            sa.Column("id", sa.Integer, primary_key=True, autoincrement=True),
            sa.Column("order_id", sa.String(36), sa.ForeignKey("orders.id", ondelete="CASCADE"), nullable=False),
            sa.Column("product_id", sa.BigInteger, nullable=False),
            sa.Column("qty", sa.Integer, nullable=False),
            sa.Column("unit_price", sa.Numeric(12, 2), nullable=False),
        )
        op.create_index("ix_order_items_order_id", "order_items", ["order_id"])

    if "payments" not in existing:
        op.create_table(
            "payments",
            sa.Column("id", sa.String(36), primary_key=True),
            sa.Column("order_id", sa.String(36), sa.ForeignKey("orders.id", ondelete="CASCADE"), nullable=False),
            sa.Column("razorpay_order_id", sa.String(64), nullable=False, unique=True),
            sa.Column("razorpay_payment_id", sa.String(64), nullable=True),
            sa.Column("status", sa.String(20), nullable=False),
            sa.Column("amount_paise", sa.BigInteger, nullable=False),
            sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        )
        op.create_index("ix_payments_order_id", "payments", ["order_id"])

    if "outbox" not in existing:
        op.create_table(
            "outbox",
            sa.Column("id", sa.BigInteger, primary_key=True, autoincrement=True),
            sa.Column("topic", sa.String(100), nullable=False),
            sa.Column("key", sa.String(64), nullable=False),
            sa.Column("event", sa.Text, nullable=False),
            sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        )


def downgrade() -> None:
    for table in ("outbox", "payments", "order_items", "orders"):
        op.drop_table(table)
