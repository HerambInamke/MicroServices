package com.provenance.catalog;

import java.util.List;
import java.util.Properties;
import java.util.concurrent.TimeUnit;
import org.apache.kafka.clients.producer.KafkaProducer;
import org.apache.kafka.clients.producer.Producer;
import org.apache.kafka.clients.producer.ProducerConfig;
import org.apache.kafka.clients.producer.ProducerRecord;
import org.apache.kafka.common.serialization.StringSerializer;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.scheduling.annotation.Scheduled;
import org.springframework.stereotype.Component;
import org.springframework.transaction.PlatformTransactionManager;
import org.springframework.transaction.support.TransactionTemplate;

/**
 * Sends outbox rows to Kafka oldest first and deletes each one Kafka acknowledged. Delivery is at least once: a
 * crash between the send and the commit resends the same envelope (same eventId), which consumers de-duplicate.
 */
@Component
public class OutboxRelay {

    /** Returns true once Kafka acknowledged the message. */
    public interface Sender {
        boolean send(String topic, String key, String value);
    }

    private record Row(long id, String topic, String key, String event) {}

    private static final Logger log = LoggerFactory.getLogger(OutboxRelay.class);

    private final JdbcTemplate jdbc;
    private final TransactionTemplate tx;
    private final Producer<String, String> producer;

    public OutboxRelay(JdbcTemplate jdbc, PlatformTransactionManager txManager, @Value("${KAFKA_BROKERS:}") String brokers) {
        this.jdbc = jdbc;
        this.tx = new TransactionTemplate(txManager);
        if (brokers.isBlank()) {
            log.warn("KAFKA_BROKERS not set: product events stay in the outbox");
            this.producer = null;
            return;
        }
        Properties p = new Properties();
        p.put(ProducerConfig.BOOTSTRAP_SERVERS_CONFIG, brokers);
        p.put(ProducerConfig.ACKS_CONFIG, "all");
        p.put(ProducerConfig.ENABLE_IDEMPOTENCE_CONFIG, true);
        p.put(ProducerConfig.MAX_BLOCK_MS_CONFIG, 5000);
        p.put(ProducerConfig.KEY_SERIALIZER_CLASS_CONFIG, StringSerializer.class);
        p.put(ProducerConfig.VALUE_SERIALIZER_CLASS_CONFIG, StringSerializer.class);
        this.producer = new KafkaProducer<>(p);
    }

    @Scheduled(fixedDelay = 1000)
    void tick() {
        if (producer == null) {
            return;
        }
        try {
            while (relayOnce(this::sendToKafka) > 0) {
                // keep going while there is a backlog
            }
        } catch (RuntimeException e) { // e.g. the database is restarting; try again on the next tick
            log.error("outbox relay failed", e);
        }
    }

    /**
     * One batch in one transaction. FOR UPDATE keeps the rows locked until it commits, so a second instance waits
     * instead of sending later events first. Stops at the first failure so nothing overtakes it. Returns how many
     * were sent.
     */
    int relayOnce(Sender sender) {
        return tx.execute(status -> {
            List<Row> rows = jdbc.query("SELECT id, topic, event_key, event FROM outbox ORDER BY id LIMIT 100 FOR UPDATE",
                    (rs, i) -> new Row(rs.getLong(1), rs.getString(2), rs.getString(3), rs.getString(4)));
            int sent = 0;
            for (Row r : rows) {
                if (!sender.send(r.topic(), r.key(), r.event())) {
                    break;
                }
                jdbc.update("DELETE FROM outbox WHERE id = ?", r.id());
                sent++;
            }
            return sent;
        });
    }

    private boolean sendToKafka(String topic, String key, String value) {
        try {
            producer.send(new ProducerRecord<>(topic, key, value)).get(5, TimeUnit.SECONDS);
            return true;
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            return false;
        } catch (Exception e) {
            log.warn("kafka did not take {} for {}, will retry: {}", topic, key, e.toString());
            return false;
        }
    }
}
