"""Runs migrations on the connection init_db hands over, or on DB_URL when run from the alembic CLI."""

import os

from alembic import context
from sqlalchemy import create_engine

from models import Base


def run(connection) -> None:
    # target_metadata lets `alembic revision --autogenerate` diff the database against models.py.
    context.configure(connection=connection, target_metadata=Base.metadata)
    with context.begin_transaction():
        context.run_migrations()


connection = context.config.attributes.get("connection")
if connection is not None:
    run(connection)
else:
    with create_engine(os.environ["DB_URL"]).connect() as conn:
        run(conn)
