package com.provenance.catalog;

import com.fasterxml.jackson.databind.ObjectMapper;
import java.time.Instant;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.Properties;
import java.util.UUID;
import java.util.concurrent.TimeUnit;
import org.apache.kafka.clients.producer.KafkaProducer;
import org.apache.kafka.clients.producer.Producer;
import org.apache.kafka.clients.producer.ProducerConfig;
import org.apache.kafka.clients.producer.ProducerRecord;
import org.apache.kafka.common.serialization.StringSerializer;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.stereotype.Component;

/** Publishes the shared event envelope from schemas/events.json. Never throws: the DB change is already committed. */
@Component
public class EventPublisher {

    private static final Logger log = LoggerFactory.getLogger(EventPublisher.class);

    private final ObjectMapper json;
    private final Producer<String, String> producer;

    public EventPublisher(ObjectMapper json, @Value("${KAFKA_BROKERS:}") String brokers) {
        this.json = json;
        if (brokers.isBlank()) {
            log.warn("KAFKA_BROKERS not set: product events are not published");
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

    // shortcut: published after the DB commit without an outbox, so an event is lost if Kafka is down at that moment; POST /search/reindex repairs Search.
    public void publish(String topic, String eventType, String key, Map<String, Object> payload) {
        if (producer == null) {
            return;
        }
        try {
            Map<String, Object> event = new LinkedHashMap<>();
            event.put("eventId", UUID.randomUUID().toString());
            event.put("timestamp", Instant.now().toString());
            event.put("sourceService", "wholesale-catalog");
            event.put("eventType", eventType);
            event.put("payload", payload);
            producer.send(new ProducerRecord<>(topic, key, json.writeValueAsString(event))).get(5, TimeUnit.SECONDS);
        } catch (Exception e) {
            log.error("could not publish {} for {}", eventType, key, e);
        }
    }

    static Map<String, Object> productPayload(Product p) {
        Map<String, Object> m = new LinkedHashMap<>();
        m.put("productId", p.getId());
        m.put("wholesalerId", p.getWholesalerId());
        m.put("name", p.getName());
        m.put("description", p.getDescription());
        m.put("categoryId", p.getCategoryId());
        m.put("basePrice", p.getBasePrice());
        m.put("moq", p.getMoq());
        return m;
    }
}
