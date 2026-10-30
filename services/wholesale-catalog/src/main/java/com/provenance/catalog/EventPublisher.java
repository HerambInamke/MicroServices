package com.provenance.catalog;

import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.ObjectMapper;
import java.time.Instant;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.UUID;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.stereotype.Component;
import org.springframework.transaction.annotation.Propagation;
import org.springframework.transaction.annotation.Transactional;

/**
 * Writes the shared event envelope from schemas/events.json to the outbox, inside the caller's transaction, so an
 * event exists exactly when its change commits. OutboxRelay sends it to Kafka afterwards.
 */
@Component
public class EventPublisher {

    private final ObjectMapper json;
    private final JdbcTemplate jdbc;

    public EventPublisher(ObjectMapper json, JdbcTemplate jdbc) {
        this.json = json;
        this.jdbc = jdbc;
    }

    @Transactional(propagation = Propagation.MANDATORY)
    public void publish(String topic, String eventType, String key, Map<String, Object> payload) {
        Map<String, Object> event = new LinkedHashMap<>();
        event.put("eventId", UUID.randomUUID().toString());
        event.put("timestamp", Instant.now().toString());
        event.put("sourceService", "wholesale-catalog");
        event.put("eventType", eventType);
        event.put("payload", payload);
        try {
            jdbc.update("INSERT INTO outbox (topic, event_key, event) VALUES (?, ?, ?)", topic, key, json.writeValueAsString(event));
        } catch (JsonProcessingException e) {
            throw new IllegalStateException("could not encode " + eventType, e); // payloads are plain maps; a bug
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
