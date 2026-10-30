"""Transactional outbox: an event is stored in the same transaction as the change it describes, and a relay
sends it to Kafka afterwards. An event therefore survives Kafka being down, or a crash right after the commit.

Delivery is at least once: a crash between the send and the delete resends the same envelope (same eventId),
which every consumer already de-duplicates on.
"""

import json
import logging
import time

from sqlalchemy import select, text
from sqlalchemy.orm import Session, sessionmaker

from clients import envelope
from models import OutboxEvent

log = logging.getLogger("order-payment")

# Only one relay sends at a time (across replicas), so events leave in the order they were written.
RELAY_LOCK = 0x6F7574626F78  # "outbox"


def enqueue(session: Session, topic: str, event_type: str, key: str, payload: dict) -> None:
    """Adds the event to the caller's transaction; it is sent only if that transaction commits."""
    session.add(OutboxEvent(topic=topic, key=key, event=json.dumps(envelope(event_type, payload), default=str)))


def relay_once(session_factory: sessionmaker, send, batch: int = 100) -> int:
    """Sends pending events oldest first and deletes each one Kafka acknowledged. Stops at the first failure so
    later events never overtake it. send(topic, key, value) -> bool. Returns how many were sent."""
    with session_factory() as session:
        if not session.scalar(text("SELECT pg_try_advisory_xact_lock(:k)"), {"k": RELAY_LOCK}):
            return 0  # another replica is relaying
        sent = 0
        for row in session.scalars(select(OutboxEvent).order_by(OutboxEvent.id).limit(batch)).all():
            if not send(row.topic, row.key, row.event):
                break
            session.delete(row)
            sent += 1
        session.commit()
        return sent


def run_relay(session_factory: sessionmaker, send, idle_seconds: float = 1.0) -> None:
    """Blocking loop; main.py runs it in a daemon thread."""
    while True:
        try:
            if relay_once(session_factory, send):
                continue  # more may be waiting
        except Exception:  # noqa: BLE001 - e.g. the database is restarting; try again next round
            log.exception("outbox relay failed")
        time.sleep(idle_seconds)
