"""Add offer_id to order items: the accepted chat offer that priced the line, usable once.

Revision ID: 0002
Revises: 0001
"""

import sqlalchemy as sa
from alembic import op

revision = "0002"
down_revision = "0001"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.add_column("order_items", sa.Column("offer_id", sa.String(64), nullable=True))
    # Named as Postgres names an unnamed UNIQUE, which is what models.py's unique=True creates.
    op.create_unique_constraint("order_items_offer_id_key", "order_items", ["offer_id"])


def downgrade() -> None:
    op.drop_column("order_items", "offer_id")  # drops its constraint with it
